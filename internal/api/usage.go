package api

import (
	"net/http"
	"strconv"
	"time"
)

// They read the LLM call ledger (usagestore.go) for the Token Usage view.

// usageWindow reads the query's time window: from/to as unix milliseconds,
// or hours=N back from now (default 24). The bucket width follows the
// window unless bucket=hour|day is given, so a chart has 24 to ~100 bars.
func usageWindow(r *http.Request) (from, to time.Time, bucketMs int64) {
	q := r.URL.Query()
	to = time.Now()
	if v, err := strconv.ParseInt(q.Get("to"), 10, 64); err == nil && v > 0 {
		to = time.UnixMilli(v)
	}
	hours := 24
	if v, err := strconv.Atoi(q.Get("hours")); err == nil && v > 0 {
		hours = v
	}
	from = to.Add(-time.Duration(hours) * time.Hour)
	if v, err := strconv.ParseInt(q.Get("from"), 10, 64); err == nil && v > 0 {
		from = time.UnixMilli(v)
	}

	hour := int64(time.Hour / time.Millisecond)
	switch q.Get("bucket") {
	case "hour":
		bucketMs = hour
	case "day":
		bucketMs = 24 * hour
	default:
		if to.Sub(from) > 4*24*time.Hour {
			bucketMs = 24 * hour
		} else {
			bucketMs = hour
		}
	}
	return from, to, bucketMs
}

// usageHandler implements GET /api/usage: token usage cells for the window.
func usageHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyAgentScoped(w, r) {
			return
		}
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		from, to, bucketMs := usageWindow(r)
		buckets, err := store.UsageSeries(from, to, bucketMs, narrowTenants(roleInfoOrOpen(r.Context()).Tenants, r.URL.Query().Get("tenant")))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to read usage: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"from":     from.UnixMilli(),
			"to":       to.UnixMilli(),
			"bucketMs": bucketMs,
			"buckets":  buckets,
		}, "error": nil})
	}
}

// usageCallsHandler implements GET /api/usage/calls: the most expensive
// calls in the window, by total tokens (limit, default 50, at most 500).
// With group=session it ranks sessions instead (TopUsageSessions).
func usageCallsHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyAgentScoped(w, r) {
			return
		}
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		from, to, _ := usageWindow(r)
		limit := 50
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
			limit = min(v, 500)
		}
		if r.URL.Query().Get("group") == "session" {
			sessions, err := store.TopUsageSessions(from, to, limit, narrowTenants(roleInfoOrOpen(r.Context()).Tenants, r.URL.Query().Get("tenant")))
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to read usage sessions: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": sessions, "error": nil})
			return
		}
		calls, err := store.TopUsageCalls(from, to, limit, narrowTenants(roleInfoOrOpen(r.Context()).Tenants, r.URL.Query().Get("tenant")))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to read usage calls: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": calls, "error": nil})
	}
}
