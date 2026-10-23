package api

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// Sessions of an agent that works in turns are listed by agent and tenant whether or not the sender declared a
// thread; a plain session without turns is not.
func TestActivityListsTurnBasedSessionsWithoutADeclaredThread(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	now := time.Now().UnixMilli()
	sep := indexSep
	for _, stmt := range []struct {
		id, agents, tenants string
		turns               any
		start, end          int64
	}{
		{"spot-1", sep + "spot-agent" + sep, sep + "bot/default" + sep, 27, now - 3*minute, now - 2*minute},
		{"spot-2", sep + "spot-agent" + sep, sep + "bot/default" + sep, 2, now - minute, now},
		{"oneshot", sep + "batch-agent" + sep, sep + "bot/default" + sep, 0, now - minute, now},
		{"unknown", sep + "other" + sep, sep + "bot/default" + sep, nil, now - minute, now},
	} {
		if _, err := archive.db.Exec(`INSERT INTO session_index (session_id, started_ms, complete, llm_calls, tenants, agents, search, thread, part, first_ms, ended_ms, turns)
			VALUES (?, ?, 1, 1, ?, ?, '', ?, 1, ?, ?, ?)`, stmt.id, stmt.start, stmt.tenants, stmt.agents, stmt.id, stmt.start, stmt.end, stmt.turns); err != nil {
			t.Fatal(err)
		}
	}
	s := NewSessionStoreWithArchive(nil, archive, SessionLimits{})

	got := s.Activity(mcptools.SessionFilter{}, RoleInfo{})
	if len(got) != 1 {
		t.Fatalf("subjects = %+v, want only the agent whose sessions have turns", got)
	}
	if a := got[0]; a.Agent != "spot-agent" || a.Tenant != "bot/default" || a.Sessions != 2 || a.Turns != 29 {
		t.Errorf("subject = %+v, want spot-agent in bot/default, 2 sessions, 29 turns", a)
	}
	if got := s.Activity(mcptools.SessionFilter{Tenant: "other/default"}, RoleInfo{}); len(got) != 0 {
		t.Errorf("a tenant pick that matches nothing listed %+v", got)
	}
	// An archive row written before turns were counted has a NULL count, which the backfill reads as "index again".
	var nulls int
	if err := archive.db.QueryRow(`SELECT COUNT(*) FROM session_index WHERE turns IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Errorf("NULL turn rows = %d err=%v, want 1", nulls, err)
	}
}
