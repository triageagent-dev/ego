package api

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSessionPersistence_RoundTrip proves the actual restart scenario the
// archive exists for: ingest into one store backed by a SQLite file, close
// it, open a fresh store against the same file, and confirm the session
// (including its spans, not just its summary) comes back.
func TestSessionPersistence_RoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	archive1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	store1 := NewSessionStoreWithArchive(nil, archive1, SessionLimits{})

	store1.Ingest(sampleTracesBody("persisted-sess", "aaaa", "span-a"))
	if err := store1.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("closing store1: %v", err)
	}

	archive2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopening archive: %v", err)
	}
	defer archive2.Close()
	store2 := NewSessionStoreWithArchive(nil, archive2, SessionLimits{})
	if err := store2.LoadPersisted(); err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}

	sessions := store2.List()
	if len(sessions) != 1 {
		t.Fatalf("got %d restored sessions, want 1", len(sessions))
	}
	if sessions[0].SessionID != "persisted-sess" {
		t.Errorf("session ID = %q, want %q", sessions[0].SessionID, "persisted-sess")
	}
	if sessions[0].SpanCount != 1 {
		t.Errorf("span count = %d, want 1", sessions[0].SpanCount)
	}

	tr, ok := store2.Trace("persisted-sess")
	if !ok {
		t.Fatal("expected restored session's trace to be retrievable")
	}
	if len(tr.AllSpans) != 1 || tr.AllSpans[0].SpanID != "span-a" {
		t.Errorf("restored spans = %v, want [span-a]", tr.AllSpans)
	}
}

// TestSessionPersistence_RestoredIncompleteSessionEventuallyCompletes
// reproduces a bug found via a live side-by-side comparison: a session
// that was still active (IsComplete=false) when persisted, and whose
// producer had already stopped sending spans before the process
// restarted, came back from the archive with no timer driving it toward
// completion - its duration ticked in the UI forever, across further
// restarts, since nothing would ever call resetIdleTimerLocked again.
// LoadPersisted must kick off a fresh idle timer for exactly this case.
func TestSessionPersistence_RestoredIncompleteSessionEventuallyCompletes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	archive1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	store1 := NewSessionStoreWithArchive(nil, archive1, SessionLimits{})
	store1.completionGrace = time.Hour // never fires: persist while still "active"

	store1.Ingest(sampleTracesBody("stale-sess", "aaaa", "span-a"))
	if store1.List()[0].IsComplete {
		t.Fatal("test setup: session must be incomplete when persisted")
	}
	if err := store1.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("closing store1: %v", err)
	}

	archive2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopening archive: %v", err)
	}
	defer archive2.Close()
	store2 := NewSessionStoreWithArchive(nil, archive2, SessionLimits{})
	store2.idleTimeout = 10 * time.Millisecond
	if err := store2.LoadPersisted(); err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}

	waitForCondition(t, time.Second, func() bool {
		return store2.List()[0].IsComplete
	})
}

// TestSQLiteStore_Delete confirms a deleted session no longer loads back.
func TestSQLiteStore_Delete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	archive, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	defer archive.Close()

	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.Ingest(sampleTracesBody("to-delete", "aaaa", "span-a"))
	if err := store.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}

	if err := archive.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok, err := archive.Load("to-delete")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ok {
		t.Error("deleted session still present in archive")
	}
}

// TestFlushPersist_SkipsWhenNothingChanged confirms flushPersist's fast
// path: once every session has been durably saved, calling it again with
// no further mutations does no work at all (changeSeq == lastPersistedSeq
// short-circuits before any snapshot/marshal/DB round trip).
func TestFlushPersist_SkipsWhenNothingChanged(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	archive, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	defer archive.Close()

	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.Ingest(sampleTracesBody("sess-a", "aaaa", "span-a"))

	if err := store.flushPersist(); err != nil {
		t.Fatalf("first flushPersist: %v", err)
	}
	seqAfterFirst := store.lastPersistedSeq
	if seqAfterFirst == 0 {
		t.Fatal("want lastPersistedSeq to advance after a real flush")
	}
	persistedSeqAfterFirst := store.persistedSeq["sess-a"]

	if err := store.flushPersist(); err != nil {
		t.Fatalf("second flushPersist: %v", err)
	}
	if store.lastPersistedSeq != seqAfterFirst {
		t.Errorf("lastPersistedSeq changed on a no-op flush: got %d, want unchanged %d", store.lastPersistedSeq, seqAfterFirst)
	}
	if store.persistedSeq["sess-a"] != persistedSeqAfterFirst {
		t.Errorf("persistedSeq[sess-a] changed on a no-op flush: got %d, want unchanged %d", store.persistedSeq["sess-a"], persistedSeqAfterFirst)
	}
}

// TestFlushPersist_OnlyPersistsChangedSession confirms flushPersist skips
// re-saving a session that hasn't changed even when a sibling session in
// the same store has - the scenario the reviewer flagged (one active
// session amid many long-completed ones re-marshaled/rewritten on every
// tick for no reason).
func TestFlushPersist_OnlyPersistsChangedSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	archive, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	defer archive.Close()

	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.Ingest(sampleTracesBody("sess-a", "aaaa", "span-a"))
	store.Ingest(sampleTracesBody("sess-b", "bbbb", "span-b"))
	if err := store.flushPersist(); err != nil {
		t.Fatalf("first flushPersist: %v", err)
	}
	seqA := store.persistedSeq["sess-a"]
	seqB := store.persistedSeq["sess-b"]

	// Only sess-a changes further.
	store.Ingest(sampleTracesBodyWithParent("sess-a", "aaaa", "span-a2", "span-a"))
	if err := store.flushPersist(); err != nil {
		t.Fatalf("second flushPersist: %v", err)
	}

	if store.persistedSeq["sess-a"] <= seqA {
		t.Errorf("persistedSeq[sess-a] did not advance after sess-a changed: got %d, want > %d", store.persistedSeq["sess-a"], seqA)
	}
	if store.persistedSeq["sess-b"] != seqB {
		t.Errorf("persistedSeq[sess-b] changed even though sess-b was untouched: got %d, want unchanged %d", store.persistedSeq["sess-b"], seqB)
	}
}

// TestSessionStore_NoArchiveIsNoop confirms the store works exactly as
// before when no SQLite path is configured (LoadPersisted/StartPersistence/
// Close all become no-ops).
func TestSessionStore_NoArchiveIsNoop(t *testing.T) {
	store := NewSessionStore(nil)
	if err := store.LoadPersisted(); err != nil {
		t.Errorf("LoadPersisted with no archive: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("Close with no archive: %v", err)
	}
}
