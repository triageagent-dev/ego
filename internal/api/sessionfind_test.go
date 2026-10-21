package api

import (
	"path/filepath"
	"testing"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// A search reaches sessions the store has evicted from memory: they are read back from the archive and
// returned next to the ones in memory, and the role limit still applies to them.
func TestFind_SearchReachesTheArchive(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	defer archive.Close()
	// Memory holds one completed session; the other is evicted after the flush.
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{Memory: 1})
	store.Ingest(sampleTracesBody("old-sess-needle", "aaaa", "span-a"))
	store.Ingest(sampleTracesBody("new-sess", "bbbb", "span-b"))
	store.completeSession("old-sess-needle")
	store.completeSession("new-sess")
	// Eviction runs after a flush: the completed sessions beyond the limit leave memory for the archive.
	if err := store.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}
	if list := store.List(); len(list) != 1 {
		t.Fatalf("expected the memory limit to leave one session in memory, got %d", len(list))
	}

	all := func(SessionSummary) bool { return true }
	got := store.Find(mcptools.SessionFilter{Query: "needle"}, all)
	if len(got) != 1 || got[0].SessionID != "old-sess-needle" {
		t.Fatalf("Find(needle) = %+v, want the archived session", got)
	}
	// Without a query the list is memory only, as before.
	if got := store.Find(mcptools.SessionFilter{}, all); len(got) != 1 || got[0].SessionID != "new-sess" {
		t.Fatalf("Find() = %+v, want the session in memory only", got)
	}
	// The role check runs on archived hits too.
	none := func(SessionSummary) bool { return false }
	if got := store.Find(mcptools.SessionFilter{Query: "needle"}, none); len(got) != 0 {
		t.Fatalf("Find(needle) for a role that sees nothing = %+v, want none", got)
	}
}

// scored: true lists the sessions online evaluation scored, including one evicted from memory, and every summary
// says whether it was scored; scored: false keeps the others.
func TestFind_ScoredFilter(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{Memory: 1})
	store.Ingest(sampleTracesBody("scored-old", "aaaa", "span-a"))
	store.Ingest(sampleTracesBody("plain-new", "bbbb", "span-b"))
	store.completeSession("scored-old")
	store.completeSession("plain-new")
	if err := store.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}
	if _, err := archive.db.Exec(`INSERT INTO online_scores (session_id, at_ms, tenant, kind, version, model, metric, score, status)
		VALUES ('scored-old', 1000, 't', 'k', 'v', 'm', 'jev_grounded', 0.5, 'decided')`); err != nil {
		t.Fatalf("inserting a score: %v", err)
	}

	all := func(SessionSummary) bool { return true }
	yes, no := true, false
	got := store.Find(mcptools.SessionFilter{Scored: &yes}, all)
	if len(got) != 1 || got[0].SessionID != "scored-old" || !got[0].Scored {
		t.Fatalf("Find(scored) = %+v, want the evicted scored session, flagged", got)
	}
	got = store.Find(mcptools.SessionFilter{Scored: &no}, all)
	if len(got) != 1 || got[0].SessionID != "plain-new" || got[0].Scored {
		t.Fatalf("Find(unscored) = %+v, want the session in memory, unflagged", got)
	}
}

// The paged list takes the same scored filter, in memory and in the archive, and flags its items.
func TestPage_ScoredFilter(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{Memory: 1})
	store.Ingest(sampleTracesBody("scored-old", "aaaa", "span-a"))
	store.Ingest(sampleTracesBody("plain-new", "bbbb", "span-b"))
	store.completeSession("scored-old")
	store.completeSession("plain-new")
	if err := store.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}
	if _, err := archive.db.Exec(`INSERT INTO online_scores (session_id, at_ms, tenant, kind, version, model, metric, score, status)
		VALUES ('scored-old', 1000, 't', 'k', 'v', 'm', 'jev_grounded', 0.5, 'decided')`); err != nil {
		t.Fatalf("inserting a score: %v", err)
	}
	all := func(SessionSummary) bool { return true }
	yes := true
	page, err := store.Page(SessionPageRequest{Filter: mcptools.SessionFilter{Scored: &yes}, Limit: 20}, all)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SessionID != "scored-old" || !page.Items[0].Scored {
		t.Fatalf("Page(scored) = %+v, want the evicted scored session, flagged", page.Items)
	}
	page, err = store.Page(SessionPageRequest{Limit: 20}, all)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	flags := map[string]bool{}
	for _, it := range page.Items {
		flags[it.SessionID] = it.Scored
	}
	if len(page.Items) != 2 || !flags["scored-old"] || flags["plain-new"] {
		t.Fatalf("Page() flags = %v, want scored-old true and plain-new false", flags)
	}
}
