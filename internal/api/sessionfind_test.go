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
