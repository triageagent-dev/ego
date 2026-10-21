package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// pagedFixture archives n sessions, session i started i minutes after base and labelled tenant a (even i) or b
// (odd i), and keeps only the newest keepInMemory of them in memory, as a long-running server does.
func pagedFixture(t *testing.T, n, keepInMemory int) (*SessionStore, *SQLiteStore, []string, time.Time) {
	t.Helper()
	archive, err := NewSQLiteStore(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.Close() })
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < n; i++ {
		label := "a"
		if i%2 == 1 {
			label = "b"
		}
		store.IngestFor("rc", labelledTracesBody(fmt.Sprintf("s%02d", i), fmt.Sprintf("%032x", i+1), fmt.Sprintf("%016x", i+1), label))
	}
	var ids []string
	store.mu.Lock()
	for id, sess := range store.sessions {
		var i int
		fmt.Sscanf(sess.ID[len(sess.ID)-2:], "%d", &i)
		sess.StartedAt = base.Add(time.Duration(i) * time.Minute)
		ids = append(ids, id)
	}
	store.mu.Unlock()
	// s00 ... s(n-1): oldest first (the tenant label is part of the id, so a plain sort would group by it).
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(a[len(a)-2:], b[len(b)-2:]) })
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	for _, id := range ids[:len(ids)-keepInMemory] {
		delete(store.sessions, id)
		store.order = slices.DeleteFunc(store.order, func(o string) bool { return o == id })
	}
	store.mu.Unlock()
	return store, archive, ids, base
}

func everyone(SessionSummary) bool { return true }

// walk follows next cursors until the end and returns the session ids in the order served.
func walk(t *testing.T, store *SessionStore, req SessionPageRequest, allow func(SessionSummary) bool) []string {
	t.Helper()
	var got []string
	for i := 0; i < 100; i++ {
		page, err := store.Page(req, allow)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > req.Limit {
			t.Fatalf("page of %d with limit %d", len(page.Items), req.Limit)
		}
		for _, s := range page.Items {
			got = append(got, s.SessionID)
		}
		if page.Next == "" {
			return got
		}
		req.Cursor = page.Next
	}
	t.Fatal("paging did not end")
	return nil
}

func TestSessionPagePagesMemoryAndArchiveNewestFirst(t *testing.T) {
	store, _, ids, _ := pagedFixture(t, 11, 3)

	got := walk(t, store, SessionPageRequest{Limit: 4}, everyone)
	want := slices.Clone(ids)
	slices.Reverse(want)
	if !slices.Equal(got, want) {
		t.Fatalf("pages served\n%v\nwant\n%v", got, want)
	}
	if first, err := store.Page(SessionPageRequest{Limit: 4}, everyone); err != nil || first.Next == "" || len(first.Items) != 4 {
		t.Fatalf("first page = %d items, next %q, err %v", len(first.Items), first.Next, err)
	}
}

func TestSessionPageFiltersRoleTenantQuerySince(t *testing.T) {
	store, _, ids, base := pagedFixture(t, 10, 2)

	tenantOf := func(want string) []string {
		return walk(t, store, SessionPageRequest{Limit: 3, Filter: mcptools.SessionFilter{Tenant: want}}, everyone)
	}
	if got := tenantOf("rc/a"); len(got) != 5 {
		t.Errorf("tenant rc/a: %d sessions, want 5", len(got))
	}
	if got := tenantOf("rc/"); len(got) != 10 {
		t.Errorf("sender-wide pick: %d sessions, want 10", len(got))
	}

	role := scopedToTenants("rc/b")
	if got := walk(t, store, SessionPageRequest{Limit: 2}, allowedBy(role)); len(got) != 5 {
		t.Errorf("a role limited to rc/b saw %d sessions, want 5", len(got))
	}
	if got := walk(t, store, SessionPageRequest{Limit: 2, Filter: mcptools.SessionFilter{Tenant: "rc/a"}}, allowedBy(role)); len(got) != 0 {
		t.Errorf("a limited role asking for another tenant got %v", got)
	}

	if got := walk(t, store, SessionPageRequest{Limit: 5, Filter: mcptools.SessionFilter{Query: "S03"}}, everyone); len(got) != 1 || got[0] != ids[3] {
		t.Errorf("query s03 = %v, want [%s]", got, ids[3])
	}
	if got := walk(t, store, SessionPageRequest{Limit: 5, Filter: mcptools.SessionFilter{Since: base.Add(6*time.Minute + time.Second)}}, everyone); len(got) != 3 {
		t.Errorf("since: %d sessions, want 3 (s07 to s09)", len(got))
	}
}

func TestSessionPageCursorSurvivesNewArrivals(t *testing.T) {
	store, _, ids, _ := pagedFixture(t, 8, 2)

	first, err := store.Page(SessionPageRequest{Limit: 3}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	// Newer sessions arrive while the reader is on page one: page two must neither repeat nor skip.
	for i := 20; i < 23; i++ {
		store.IngestFor("rc", labelledTracesBody(fmt.Sprintf("s%02d", i), fmt.Sprintf("%032x", i+1), fmt.Sprintf("%016x", i+1), "a"))
	}
	second, err := store.Page(SessionPageRequest{Limit: 3, Cursor: first.Next}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range second.Items {
		got = append(got, s.SessionID)
	}
	want := []string{ids[4], ids[3], ids[2]}
	if !slices.Equal(got, want) {
		t.Errorf("second page %v, want %v", got, want)
	}
}

func TestSessionPageStopsAtScanCapWithACursor(t *testing.T) {
	store, _, ids, _ := pagedFixture(t, 7, 0)
	oldChunk, oldCap := pageScanChunk, pageScanCap
	pageScanChunk, pageScanCap = 2, 2
	t.Cleanup(func() { pageScanChunk, pageScanCap = oldChunk, oldCap })

	// A role that sees only tenant b (s01, s03, s05): the role is checked in Go, so each request reads two index
	// rows and may hand back fewer sessions than asked for plus a cursor. The walk still finds all three.
	allow := allowedBy(scopedToTenants("rc/b"))
	first, err := store.Page(SessionPageRequest{Limit: 5}, allow)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) >= 3 || first.Next == "" {
		t.Fatalf("first request = %d items, next %q; want a short page that continues", len(first.Items), first.Next)
	}
	got := walk(t, store, SessionPageRequest{Limit: 5}, allow)
	if !slices.Equal(got, []string{ids[5], ids[3], ids[1]}) {
		t.Errorf("walk found %v, want %v", got, []string{ids[5], ids[3], ids[1]})
	}
}

func TestSessionPageRejectsABadCursor(t *testing.T) {
	store, _, _, _ := pagedFixture(t, 2, 2)
	if _, err := store.Page(SessionPageRequest{Limit: 2, Cursor: "not a cursor"}, everyone); err == nil {
		t.Error("a malformed cursor was accepted")
	}
}

func TestSessionIndexBackfillAndPrune(t *testing.T) {
	store, archive, ids, _ := pagedFixture(t, 6, 0)

	// An archive written before the index existed: the rows are missing, the sessions are there.
	if _, err := archive.db.Exec("DELETE FROM session_index"); err != nil {
		t.Fatal(err)
	}
	if got := walk(t, store, SessionPageRequest{Limit: 3}, everyone); len(got) != 0 {
		t.Fatalf("an unindexed archive listed %v", got)
	}
	n, err := archive.BackfillSessionIndex(DefaultTenantAttr)
	if err != nil || n != 6 {
		t.Fatalf("backfill = %d, %v; want 6", n, err)
	}
	if got := walk(t, store, SessionPageRequest{Limit: 3}, everyone); len(got) != 6 {
		t.Fatalf("after the backfill %v", got)
	}
	if n, _ := archive.BackfillSessionIndex(DefaultTenantAttr); n != 0 {
		t.Errorf("a second backfill added %d", n)
	}

	if err := archive.Delete(ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.PruneSessions(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := archive.db.QueryRow("SELECT COUNT(*) FROM session_index").Scan(&left); err != nil || left != 0 {
		t.Errorf("index rows left after prune = %d, %v", left, err)
	}
}

func TestSessionsHandlerPagesWithLimitAndCursor(t *testing.T) {
	store, _, ids, _ := pagedFixture(t, 5, 1)
	get := func(query string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		sessionsHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/streaming/sessions?"+query, nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := get("limit=2")
	data, _ := out["data"].([]any)
	next, _ := out["next"].(string)
	if code != http.StatusOK || len(data) != 2 || next == "" {
		t.Fatalf("first page: %d, %d items, next %q", code, len(data), next)
	}
	if first, _ := data[0].(map[string]any); first["sessionId"] != ids[4] {
		t.Errorf("newest session first: got %v, want %s", first["sessionId"], ids[4])
	}
	code, out = get("limit=2&cursor=" + url.QueryEscape(next))
	if data, _ = out["data"].([]any); code != http.StatusOK || len(data) != 2 {
		t.Errorf("second page: %d, %d items", code, len(data))
	}
	if code, _ = get("limit=0"); code != http.StatusBadRequest {
		t.Errorf("limit=0 got %d, want 400", code)
	}
	if code, _ = get("limit=2&cursor=%25%25"); code != http.StatusBadRequest {
		t.Errorf("a bad cursor got %d, want 400", code)
	}
	// Without limit the answer is the whole list, as before.
	if _, out = get(""); out["next"] != nil {
		t.Errorf("an unpaged answer carries a cursor: %v", out["next"])
	}
}

func TestSessionPageTotalOnFirstPageOnly(t *testing.T) {
	store, _, _, _ := pagedFixture(t, 11, 3)

	first, err := store.Page(SessionPageRequest{Limit: 4, Unrestricted: true}, everyone)
	if err != nil || !first.TotalKnown || first.Total != 11 || first.TotalApprox {
		t.Fatalf("first page total = %d known %v approx %v, err %v; want 11 exact", first.Total, first.TotalKnown, first.TotalApprox, err)
	}
	second, _ := store.Page(SessionPageRequest{Limit: 4, Cursor: first.Next, Unrestricted: true}, everyone)
	if second.TotalKnown {
		t.Error("a later page carries a total")
	}

	// A filter SQL decides, and one only Go can (a role): both count the same sessions the walk serves.
	byTenant, _ := store.Page(SessionPageRequest{Limit: 4, Unrestricted: true, Filter: mcptools.SessionFilter{Tenant: "rc/a"}}, everyone)
	if byTenant.Total != 6 {
		t.Errorf("tenant rc/a total = %d, want 6", byTenant.Total)
	}
	limited, _ := store.Page(SessionPageRequest{Limit: 4}, allowedBy(scopedToTenants("rc/b")))
	if !limited.TotalKnown || limited.Total != 5 || limited.TotalApprox {
		t.Errorf("limited role total = %d approx %v, want 5 exact", limited.Total, limited.TotalApprox)
	}

	// Past the count cap the number is a lower bound.
	oldChunk, oldCap := pageScanChunk, countScanCap
	pageScanChunk, countScanCap = 2, 4
	t.Cleanup(func() { pageScanChunk, countScanCap = oldChunk, oldCap })
	capped, _ := store.Page(SessionPageRequest{Limit: 4}, allowedBy(scopedToTenants("rc/b")))
	if !capped.TotalApprox || capped.Total < 1 {
		t.Errorf("capped count = %d approx %v, want a lower bound", capped.Total, capped.TotalApprox)
	}
}
