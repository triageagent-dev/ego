package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/online"
)

func rowCount(t *testing.T, a *SQLiteStore, q string, args ...any) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// withDocuments is a session whose model span carries one context document.
func withDocuments(trace, text string) map[string]any {
	body := onlineSessionBody(trace, time.Now().UnixMilli(), "rca", "question", "answer")
	rs := body["resourceSpans"].([]any)[0].(map[string]any)
	span := rs["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[1].(map[string]any)
	span["attributes"] = append(span["attributes"].([]any), map[string]any{
		"key": "ego.evidence.documents", "value": map[string]any{"stringValue": `[{"id":"1","content":"` + text + `"}]`},
	})
	return body
}

// A purge takes a session's context documents with it, whether or not they were written yet, and leaves no text of
// them behind.
func TestPurgeRemovesContextDocumentTexts(t *testing.T) {
	for name, flushFirst := range map[string]bool{"still buffered": false, "written": true} {
		t.Run(name, func(t *testing.T) {
			_, sessions, archive, _ := newTestEngine(t, testOnlineConfig)
			trace := "dddddddddddd1111"
			id := "otlp-" + shortID(trace)
			sessions.Ingest(withDocuments(trace, "a customer record"))
			if flushFirst {
				if err := sessions.flushPersist(); err != nil {
					t.Fatal(err)
				}
				if rowCount(t, archive, `SELECT COUNT(*) FROM evidence_blobs`) != 1 {
					t.Fatal("no document to start from")
				}
			}
			if _, err := sessions.PurgeSessions([]string{id}, false); err != nil {
				t.Fatal(err)
			}
			if err := sessions.flushPersist(); err != nil {
				t.Fatal(err)
			}
			if refs, texts := rowCount(t, archive, `SELECT COUNT(*) FROM evidence_refs`), rowCount(t, archive, `SELECT COUNT(*) FROM evidence_blobs`); refs != 0 || texts != 0 {
				t.Errorf("after the purge: %d references, %d texts", refs, texts)
			}
		})
	}
}

// A text two sessions share stays for the one that was not purged.
func TestPurgeKeepsADocumentAnotherSessionRefersTo(t *testing.T) {
	_, sessions, archive, _ := newTestEngine(t, testOnlineConfig)
	a, b := "aaaaaaaaaaaa1111", "bbbbbbbbbbbb2222"
	sessions.Ingest(withDocuments(a, "shared text"))
	sessions.Ingest(withDocuments(b, "shared text"))
	if err := sessions.flushPersist(); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.PurgeSessions([]string{"otlp-" + shortID(a)}, false); err != nil {
		t.Fatal(err)
	}
	docs, err := archive.SessionEvidence("otlp-" + shortID(b))
	if err != nil || len(docs) != 1 {
		t.Errorf("the other session's documents = %v, %v", docs, err)
	}
}

// Texts nothing refers to are pruned even when sessions are kept forever.
func TestOrphanDocumentsArePrunedWithoutRetention(t *testing.T) {
	_, sessions, archive, _ := newTestEngine(t, testOnlineConfig)
	trace := "cccccccccccc3333"
	sessions.Ingest(withDocuments(trace, "dropped text"))
	if err := sessions.flushPersist(); err != nil {
		t.Fatal(err)
	}
	sessions.DropEvidence("otlp-" + shortID(trace))
	sessions.sessionRetention = 0
	sessions.lastSessionPrune = time.Time{}
	sessions.pruneSessions()
	if texts := rowCount(t, archive, `SELECT COUNT(*) FROM evidence_blobs`); texts != 0 {
		t.Errorf("%d texts left", texts)
	}
	if rowCount(t, archive, `SELECT COUNT(*) FROM sessions`) == 0 {
		t.Error("retention 0 removed a session")
	}
}

// purgingJudge runs during once, while the session it judges is being scored.
type purgingJudge struct {
	during func()
	on     func(int64)
}

func (j *purgingJudge) Generate(context.Context, string) (string, error) {
	if j.during != nil {
		f := j.during
		j.during = nil
		f()
	}
	j.on(100)
	return "ID: rubric_0\nProperty: helpful\nRationale: fine\nVerdict: yes\n", nil
}

// A purge that happens while a session is being judged wins: nothing of the session is written afterwards.
func TestPurgeDuringScoringLeavesNothing(t *testing.T) {
	e, sessions, archive, _ := newTestEngine(t, testOnlineConfig)
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	trace := "abababababab0004"
	id := "otlp-" + shortID(trace)
	sessions.Ingest(onlineSessionBody(trace, now.UnixMilli(), "rca", "why is checkout down?", "The payment service is out of memory."))
	judged := false
	pj := &purgingJudge{}
	e.newJudge = func(_ context.Context, _ string, on func(int64)) (judge.Model, error) { pj.on = on; return pj, nil }
	pj.during = func() {
		judged = true
		if _, err := e.PurgeSessions(false, func() (PurgeResult, error) { return sessions.PurgeSessions([]string{id}, false) }); err != nil {
			t.Error(err)
		}
	}
	e.process(context.Background(), id)
	if !judged {
		t.Fatal("the session was never judged")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM online_evals WHERE session_id = ?`,
		`SELECT COUNT(*) FROM online_scores WHERE session_id = ?`,
		`SELECT COUNT(*) FROM run_results WHERE eval_set_item_id = ?`,
		`SELECT COUNT(*) FROM sessions WHERE session_id = ?`,
	} {
		if n := rowCount(t, archive, q, id); n != 0 {
			t.Errorf("%d rows left: %s", n, q)
		}
	}
	if e.budget.Status(now).Spent.JudgeTokens == 0 {
		t.Error("the tokens spent on it are not charged")
	}
}

// A purge removes what quotes the session: its run results and labels, and the text of a finding that rests on it.
func TestPurgeRemovesWhatQuotesTheSession(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	ids := f.seedBad("acme", 9)
	f.e.findingsMaintain(context.Background(), f.now)
	open := f.findings(t, "open")
	if len(open) != 1 || len(open[0].Sessions) == 0 {
		t.Fatalf("open findings = %+v", open)
	}
	cited := open[0].Sessions[0]
	if err := f.archive.InsertRunResults("run-x", []runResultRowDTO{
		{ResultID: "res-1", RunID: "run-x", EvalSetItemID: cited, EvaluatorName: "m", EvaluatorType: "builtin", Status: "failed", CreatedAt: nowRFC3339()},
		{ResultID: "res-2", RunID: "run-x", EvalSetItemID: "another-session", EvaluatorName: "m", EvaluatorType: "builtin", Status: "passed", CreatedAt: nowRFC3339()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.archive.InsertOnlineLabel(&online.Label{ID: "label-1", SessionID: cited, Tenant: "acme", Kind: "rca", Metric: "m", Verdict: "agree", Note: "quotes the user", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	_ = ids

	dry, err := f.sessions.PurgeSessions([]string{cited}, true)
	if err != nil || dry.Results < 1 || dry.Labels != 1 || dry.Findings != 1 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	if len(f.findings(t, "open")) != 1 || rowCount(t, f.archive, `SELECT COUNT(*) FROM online_labels`) != 1 {
		t.Fatal("a dry run changed something")
	}

	res, err := f.e.PurgeSessions(false, func() (PurgeResult, error) { return f.sessions.PurgeSessions([]string{cited}, false) })
	if err != nil || res.Findings != 1 || res.Labels != 1 || res.Results != dry.Results {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	if n := rowCount(t, f.archive, `SELECT COUNT(*) FROM run_results WHERE eval_set_item_id = ?`, cited); n != 0 {
		t.Errorf("%d results of the purged session", n)
	}
	if n := rowCount(t, f.archive, `SELECT COUNT(*) FROM run_results WHERE eval_set_item_id = 'another-session'`); n != 1 {
		t.Errorf("another session's result: %d", n)
	}
	stale := f.findings(t, "stale")
	if len(f.findings(t, "open")) != 0 || len(stale) != 1 {
		t.Fatalf("findings after: open %d stale %d", len(f.findings(t, "open")), len(stale))
	}
	d := stale[0].Diagnosis
	if d.FailureMode != scrubbedFinding || d.Summary != "" || d.SuggestedCorrection != "" || len(d.Evidence) != 0 {
		t.Errorf("the diagnosis still reads: %+v", d)
	}
	for _, s := range stale[0].Sessions {
		if s == cited {
			t.Error("the finding still names the purged session")
		}
	}
}

// A finding the model wrote while a session it quotes was purged is not stored.
func TestFindingWrittenDuringAPurgeIsDropped(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	ids := f.seedBad("acme", 9)
	f.judge.during = func() {
		if _, err := f.sessions.PurgeSessions(ids, false); err != nil {
			t.Error(err)
		}
	}
	f.e.findingsMaintain(context.Background(), f.now)
	if len(f.judge.prompts) == 0 {
		t.Fatal("no diagnosis was asked for")
	}
	if got := f.findings(t, "all"); len(got) != 0 {
		t.Errorf("stored although its evidence was purged: %+v", got)
	}
}

// The REST field is dryRun. "dry_run" is refused: ignoring it would turn a preview into the purge.
func TestPurgeAndDismissRefuseUnknownFields(t *testing.T) {
	e, sessions, archive, _ := newTestEngine(t, testOnlineConfig)
	trace := "abababababab0007"
	id := "otlp-" + shortID(trace)
	sessions.Ingest(onlineSessionBody(trace, time.Now().UnixMilli(), "rca", "q", "a"))
	if err := sessions.flushPersist(); err != nil {
		t.Fatal(err)
	}
	for path, c := range map[string]struct {
		h    http.HandlerFunc
		body string
	}{
		"/api/admin/purge-sessions":    {adminPurgeSessionsHandler(sessions, nil), `{"ids":["` + id + `"],"dry_run":true}`},
		"/api/admin/purge-tenants":     {adminPurgeTenantsHandler(sessions, nil, nil), `{"keys":["shop"],"dry_run":true}`},
		"/api/admin/merge-tenants":     {adminMergeTenantsHandler(sessions, nil, nil, nil), `{"from":"a","to":"b","dry_run":true}`},
		"/api/online/findings/dismiss": {onlineFindingsDismissHandler(e), `{"ids":["x"],"reason":"r","dry_run":true}`},
	} {
		w := httptest.NewRecorder()
		c.h(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.body)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "dry_run") {
			t.Errorf("%s = %d %s", path, w.Code, w.Body.String())
		}
	}
	if rowCount(t, archive, `SELECT COUNT(*) FROM sessions WHERE session_id = ?`, id) != 1 {
		t.Error("the session is gone")
	}
}

// Over MCP the argument is dry_run, and the mirror mistake ("dryRun", or a string) must not purge either.
func TestPurgeSessionsToolRefusesUnknownArguments(t *testing.T) {
	e, sessions, archive, _ := newTestEngine(t, testOnlineConfig)
	trace := "abababababab0008"
	id := "otlp-" + shortID(trace)
	sessions.Ingest(onlineSessionBody(trace, time.Now().UnixMilli(), "rca", "q", "a"))
	if err := sessions.flushPersist(); err != nil {
		t.Fatal(err)
	}
	h := mcpRoute("", nil, nil, "", nil, nil, newMCPBackend(sessions, archive, e, nil))
	for _, args := range []map[string]any{
		{"session_ids": []string{id}, "dryRun": true},
		{"session_ids": []string{id}, "dry_run": "true"},
	} {
		if _, res := callTool(t, h, "", "purge_sessions", args); !res.IsError {
			t.Errorf("purge_sessions %v was accepted", args)
		}
	}
	if _, res := callTool(t, h, "", "purge_sessions", map[string]any{"session_ids": []string{id}, "dry_run": true}); res.IsError {
		t.Errorf("a real dry run was refused: %+v", res)
	}
	if rowCount(t, archive, `SELECT COUNT(*) FROM sessions WHERE session_id = ?`, id) != 1 {
		t.Error("the session is gone")
	}
}
