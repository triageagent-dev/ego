package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminOverviewJoinsTenantsWithUsageAndTokens(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	defer store.Close()
	names, _ := NewTenantNames(archive)
	senders, err := NewSenderStore(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := senders.Create("triage", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := senders.Create("symbio-rag", ""); err != nil {
		t.Fatal(err)
	}
	if err := names.Set("symbio-rag/symbio-rag", "Symbio RAG"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := senders.NewToken("symbio-rag", "exporter", "symbio-rag", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := senders.NewToken("triage", "any label", "", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, r := range []struct {
		tenant  string
		errored int
		in, out int
		ageDays int
	}{
		{"triage/acme", 1, 100, 10, 1},
		{"triage/acme", 0, 50, 5, 2},
		{"triage/acme", 0, 999, 999, 30}, // outside the window
	} {
		started := now.Add(-time.Duration(r.ageDays) * 24 * time.Hour).UnixMilli()
		if _, err := archive.db.Exec(`INSERT INTO llm_calls (span_id, trace_id, session_id, start_ms, duration_ms, service, tenant, kind, model, name, input_tokens, output_tokens, is_error, thread, cache_read_tokens, cache_write_tokens)
			VALUES (?, 't', 's', ?, 1, '', ?, 'k', 'm', '', ?, ?, ?, 's', 0, 0)`, string(rune('a'+i)), started, r.tenant, r.in, r.out, r.errored); err != nil {
			t.Fatal(err)
		}
	}

	got, err := buildAdminOverview(store, archive, names, senders, now)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]adminTenantRow{}
	for _, r := range got.Tenants {
		byKey[r.Key] = r
	}
	acme := byKey["triage/acme"]
	if acme.Calls != 2 || acme.Errors != 1 || acme.InputTokens != 150 || acme.OutputTokens != 15 || acme.Sender != "triage" || acme.Label != "acme" {
		t.Errorf("triage/acme = %+v, want 2 calls, 1 error, 150 in, 15 out in the window", acme)
	}
	rag, ok := byKey["symbio-rag/symbio-rag"]
	if !ok || rag.Name != "Symbio RAG" || rag.Tokens != 1 || rag.Calls != 0 {
		t.Errorf("a tenant with a bound token and no traffic yet must be listed with its name: %+v ok=%v", rag, ok)
	}
	if len(got.Senders) != 2 || len(got.Senders[0].Tokens)+len(got.Senders[1].Tokens) != 2 {
		t.Errorf("senders = %+v", got.Senders)
	}

	// Only an admin reads it, and never a token value.
	h := adminOverviewHandler(store, archive, names, senders)
	for role, want := range map[string]int{"admin": http.StatusOK, "member": http.StatusForbidden} {
		info := RoleInfo{Role: RoleMember}
		if role == "admin" {
			info = RoleInfo{Role: RoleAdmin, IsAdmin: true}
		}
		req := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil).WithContext(asRole(info))
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", role, rec.Code, want)
		}
		if role == "admin" && strings.Contains(rec.Body.String(), "egs_") {
			t.Errorf("the overview carries a token value: %s", rec.Body.String())
		}
	}
}
