package api

import (
	"path/filepath"
	"testing"
	"time"
)

// completeAll ingests one single-span session per name and completes it.
func completeAll(t *testing.T, store *SessionStore, names ...string) {
	t.Helper()
	for i, name := range names {
		store.Ingest(sampleTracesBody(name, "trace-"+name, "span-"+name))
		store.completeSession(name)
		// distinct CompletedAt order: oldest first
		store.mu.Lock()
		at := time.Now().Add(time.Duration(i-len(names)) * time.Minute)
		store.sessions[name].CompletedAt = &at
		store.mu.Unlock()
	}
}

// TestSessionLimits_EvictsOldestCompletedAndReadsBack: with Memory 2, a
// persist leaves the two newest completed sessions in memory; the evicted
// one is still served (Trace, SpanRows) from the archive, and a new span for
// it brings it back instead of starting a second session.
func TestSessionLimits_EvictsOldestCompletedAndReadsBack(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{Memory: 2})
	completeAll(t, store, "old", "mid", "new")

	if err := store.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}
	var ids []string
	for _, s := range store.List() {
		ids = append(ids, s.SessionID)
	}
	if len(ids) != 2 || ids[0] != "mid" || ids[1] != "new" {
		t.Fatalf("in memory = %v, want [mid new]", ids)
	}

	tr, ok := store.Trace("old")
	if !ok || len(tr.AllSpans) != 1 || tr.AllSpans[0].SpanID != "span-old" {
		t.Fatalf("evicted session not served from archive: ok=%v trace=%+v", ok, tr)
	}
	if rows, ok := store.SpanRows("old"); !ok || len(rows) != 1 {
		t.Fatalf("SpanRows(old) = %v, %v", rows, ok)
	}

	store.Ingest(sampleTracesBody("old", "trace-old-2", "span-old-2"))
	tr, ok = store.Trace("old")
	if !ok || len(tr.AllSpans) != 2 {
		t.Fatalf("reopened evicted session has %d spans, want 2 (archived + new)", len(tr.AllSpans))
	}
}

// TestSessionLimits_LoadRecentNewestFirst: LoadRecent (the restart path)
// returns the newest N sessions, oldest of them first; 0 means all.
func TestSessionLimits_LoadRecentNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.db.Exec(`INSERT INTO sessions (session_id, data, updated_at) VALUES
		('a', '{"session_id":"a","is_complete":true}', '2026-01-01T00:00:00Z'),
		('b', '{"session_id":"b","is_complete":true}', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	snaps, err := archive.LoadRecent(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].SessionID != "b" {
		t.Fatalf("LoadRecent(1, 0) = %+v, want [b]", snaps)
	}
	all, err := archive.LoadRecent(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].SessionID != "a" || all[1].SessionID != "b" {
		t.Fatalf("LoadRecent(0, 0) = %+v, want [a b] oldest first", all)
	}
	// A byte budget smaller than one row still returns the newest session, and
	// one that fits a single row drops the older ones.
	for _, budget := range []int64{1, 50} {
		capped, err := archive.LoadRecent(0, budget)
		if err != nil {
			t.Fatal(err)
		}
		if len(capped) != 1 || capped[0].SessionID != "b" {
			t.Fatalf("LoadRecent(0, %d) = %+v, want [b]", budget, capped)
		}
	}
	archive.Close()
}

// TestSessionLimits_PruneDeletesOldAndCompacts: the daily prune deletes
// archived sessions not updated within the retention, drops them from
// memory, keeps newer ones, and compaction succeeds.
func TestSessionLimits_PruneDeletesOldAndCompacts(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{Retention: 24 * time.Hour})
	completeAll(t, store, "stale", "fresh")
	store.mu.Lock()
	old := time.Now().Add(-48 * time.Hour)
	store.sessions["stale"].CompletedAt = &old
	store.lastSessionPrune = time.Now() // keep flushPersist's own prune out of the way
	store.mu.Unlock()
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.db.Exec(`UPDATE sessions SET updated_at = ? WHERE session_id = 'stale'`,
		old.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	store.lastSessionPrune = time.Time{}
	store.mu.Unlock()
	store.pruneSessions()

	if _, ok, _ := archive.Load("stale"); ok {
		t.Error("stale session still archived")
	}
	if _, ok, _ := archive.Load("fresh"); !ok {
		t.Error("fresh session pruned")
	}
	if _, ok := store.Trace("stale"); ok {
		t.Error("stale session still served")
	}
	if _, ok := store.Trace("fresh"); !ok {
		t.Error("fresh session gone")
	}
}
