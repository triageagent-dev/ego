package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// adminOverviewWindow is how far back the overview counts LLM calls when the request names no window; the longest
// it will count is adminOverviewMaxWindow, the longest range the admin page offers.
const (
	adminOverviewWindow    = 7 * 24 * time.Hour
	adminOverviewMaxWindow = 30 * 24 * time.Hour
)

// adminWindow reads ?hours= as the window the figures cover: the default when absent or not a positive number, at
// most the longest the page offers.
func adminWindow(r *http.Request) time.Duration {
	h, err := strconv.Atoi(r.URL.Query().Get("hours"))
	if err != nil || h <= 0 {
		return adminOverviewWindow
	}
	return min(time.Duration(h)*time.Hour, adminOverviewMaxWindow)
}

// tenantUsage is what the usage ledger holds for one tenant in a window.
type tenantUsage struct {
	Calls        int64
	Errors       int64
	InputTokens  int64
	OutputTokens int64
	LastCall     int64 // unix milliseconds of the newest call in the window
}

// UsageByTenant totals the ledger rows from since on, per tenant key.
func (s *SQLiteStore) UsageByTenant(since time.Time) (map[string]tenantUsage, error) {
	rows, err := s.db.Query(`SELECT tenant, COUNT(*), SUM(is_error), SUM(input_tokens), SUM(output_tokens), MAX(start_ms)
		FROM llm_calls WHERE start_ms >= ? GROUP BY tenant`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]tenantUsage{}
	for rows.Next() {
		var key string
		var u tenantUsage
		if err := rows.Scan(&key, &u.Calls, &u.Errors, &u.InputTokens, &u.OutputTokens, &u.LastCall); err != nil {
			return nil, err
		}
		out[key] = u
	}
	return out, rows.Err()
}

// adminTenantRow is one tenant of the admin overview: who it belongs to and what it did in the window.
type adminTenantRow struct {
	Key          string `json:"key"`
	Sender       string `json:"sender,omitempty"`
	Label        string `json:"label"`
	Name         string `json:"name,omitempty"`
	Sessions     int    `json:"sessions"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	LastCall     int64  `json:"lastCall,omitempty"`
	// Tokens counts the ingest tokens bound to this tenant.
	Tokens int `json:"tokens"`
}

// adminSenderRow is one sender with its ingest tokens (never the token values).
type adminSenderRow struct {
	Sender
	Tokens []SenderToken `json:"tokens"`
}

type adminOverview struct {
	// Since is the start of the window the call figures cover, in unix milliseconds.
	Since   int64            `json:"since"`
	Tenants []adminTenantRow `json:"tenants"`
	Senders []adminSenderRow `json:"senders"`
}

// buildAdminOverview joins every tenant the server knows of (from sessions, from the ledger, from tokens bound to
// one) with its name, its call figures of the window and its bound tokens, and lists the senders with their tokens.
func buildAdminOverview(store *SessionStore, archive *SQLiteStore, names *TenantNames, senders *SenderStore, now time.Time, window time.Duration) (adminOverview, error) {
	since := now.Add(-window)
	out := adminOverview{Since: since.UnixMilli(), Tenants: []adminTenantRow{}, Senders: []adminSenderRow{}}
	rows := map[string]*adminTenantRow{}
	row := func(key string) *adminTenantRow {
		r, ok := rows[key]
		if !ok {
			o := newTenantOption(key, 0)
			r = &adminTenantRow{Key: key, Sender: o.Sender, Label: o.Label}
			if names != nil {
				r.Name = names.Name(key)
			}
			rows[key] = r
		}
		return r
	}
	for key, n := range store.TenantCounts() {
		row(key).Sessions = n
	}
	if archive != nil {
		usage, err := archive.UsageByTenant(since)
		if err != nil {
			return out, err
		}
		for key, u := range usage {
			if key == "" {
				continue
			}
			r := row(key)
			r.Calls, r.Errors, r.InputTokens, r.OutputTokens, r.LastCall = u.Calls, u.Errors, u.InputTokens, u.OutputTokens, u.LastCall
		}
	}
	if senders != nil {
		for _, s := range senders.List() {
			tokens := senders.Tokens(s.ID)
			out.Senders = append(out.Senders, adminSenderRow{Sender: s, Tokens: tokens})
			for _, t := range tokens {
				if t.Tenant != "" {
					row(tenantKey(s.ID, t.Tenant)).Tokens++
				}
			}
		}
	}
	for _, r := range rows {
		out.Tenants = append(out.Tenants, *r)
	}
	sort.Slice(out.Tenants, func(i, j int) bool { return strings.Compare(out.Tenants[i].Key, out.Tenants[j].Key) < 0 })
	return out, nil
}

// adminOverviewHandler implements GET /api/admin/overview: every tenant with its figures, and every sender with its
// ingest tokens, for the admin panel of the settings page. Admin only.
func adminOverviewHandler(store *SessionStore, archive *SQLiteStore, names *TenantNames, senders *SenderStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out, err := buildAdminOverview(store, archive, names, senders, time.Now(), adminWindow(r))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to read the overview: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}
