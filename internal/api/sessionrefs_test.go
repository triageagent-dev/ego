package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
)

func jsonPost(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestConvertBySessionID(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(agentTracesBody("mine", "aaaa", "s1", "a"))
	store.Ingest(agentTracesBody("theirs", "bbbb", "s2", "b"))

	rec := httptest.NewRecorder()
	convertHandler(store)(rec, jsonPost("/api/convert", `{"session_ids":["mine"]}`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"traces"`) {
		t.Fatalf("convert by id = %d %s, want 200 with traces", rec.Code, rec.Body.String())
	}

	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"unknown session": {`{"session_ids":["nope"]}`, http.StatusNotFound},
		"no sessions":     {`{"session_ids":[]}`, http.StatusBadRequest},
		"bad body":        {`{`, http.StatusBadRequest},
	} {
		rec = httptest.NewRecorder()
		convertHandler(store)(rec, jsonPost("/api/convert", tc.body))
		if rec.Code != tc.want {
			t.Errorf("%s: status %d (%s), want %d", name, rec.Code, rec.Body.String(), tc.want)
		}
	}

	ids := make([]string, maxSessionRefs+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("%q", fmt.Sprint(i))
	}
	rec = httptest.NewRecorder()
	convertHandler(store)(rec, jsonPost("/api/convert", `{"session_ids":[`+strings.Join(ids, ",")+`]}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("too many sessions: status %d, want 400", rec.Code)
	}
}

func TestConvertBySessionIDHidesOutOfScopeSessions(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(agentTracesBody("mine", "aaaa", "s1", "a"))
	store.Ingest(agentTracesBody("theirs", "bbbb", "s2", "b"))
	as := withRoleInfoForTest(scopedTo("a"))

	rec := httptest.NewRecorder()
	convertHandler(store)(rec, as(jsonPost("/api/convert", `{"session_ids":["theirs"]}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("convert of an out-of-scope session = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	convertHandler(store)(rec, as(jsonPost("/api/convert", `{"session_ids":["mine","theirs"]}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a list holding an out-of-scope session = %d, want 404 (nothing partial)", rec.Code)
	}
	rec = httptest.NewRecorder()
	convertHandler(store)(rec, as(jsonPost("/api/convert", `{"session_ids":["mine"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("convert of an in-scope session = %d, want 200", rec.Code)
	}
}

func TestParseEvaluateByID(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(agentTracesBody("mine", "aaaa", "s1", "a"))
	store.Ingest(agentTracesBody("theirs", "bbbb", "s2", "b"))

	req, status, msg := parseEvaluateRequest(jsonPost("/api/evaluate", `{"config":{"evaluators":[]},"session_ids":["mine"]}`), store, nil)
	if msg != "" || req == nil {
		t.Fatalf("parse = %d %q, want a request", status, msg)
	}
	if len(req.traces) != 1 || req.evalSet != nil {
		t.Fatalf("traces = %d, evalSet = %v; want one trace and no eval set", len(req.traces), req.evalSet)
	}
	for _, name := range req.traceFilenames {
		if name != "trace_mine.jsonl" {
			t.Errorf("trace file name = %q, want the name an upload of this session would have had", name)
		}
	}

	if _, status, _ = parseEvaluateRequest(jsonPost("/api/evaluate", `{"config":{},"session_ids":["nope"]}`), store, nil); status != http.StatusNotFound {
		t.Errorf("unknown session: status %d, want 404", status)
	}
	if _, status, _ = parseEvaluateRequest(jsonPost("/api/evaluate", `{"config":{},"session_ids":["mine"],"eval_set_session_id":"nope"}`), store, nil); status != http.StatusNotFound {
		t.Errorf("unknown eval set session: status %d, want 404", status)
	}

	// A session with no conversation cannot be a golden reference; that is a load error, not a crash.
	req, _, msg = parseEvaluateRequest(jsonPost("/api/evaluate", `{"config":{},"session_ids":["mine"],"eval_set_session_id":"mine"}`), store, nil)
	if msg != "" || req.evalSet != nil || len(req.loadErrors) != 1 {
		t.Errorf("golden without a conversation: msg=%q evalSet=%v loadErrors=%v", msg, req.evalSet, req.loadErrors)
	}

	as := withRoleInfoForTest(scopedTo("a"))
	if _, status, _ = parseEvaluateRequest(as(jsonPost("/api/evaluate", `{"config":{},"session_ids":["theirs"]}`)), store, nil); status != http.StatusNotFound {
		t.Errorf("out-of-scope session: status %d, want 404", status)
	}
	if _, status, _ = parseEvaluateRequest(as(jsonPost("/api/evaluate", `{"config":{},"session_ids":["mine"],"eval_set_session_id":"theirs"}`)), store, nil); status != http.StatusNotFound {
		t.Errorf("out-of-scope eval set session: status %d, want 404", status)
	}
}

func TestParseEvaluateByID_SavedEvalSetStaysOnTheServer(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(agentTracesBody("mine", "aaaa", "s1", "a"))
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.Close() })
	if _, err := archive.SaveEvalSet(adk.EvalSet{EvalSetID: "saved", EvalCases: []adk.EvalCase{{EvalID: "c1"}}}); err != nil {
		t.Fatal(err)
	}
	body := `{"config":{"evaluators":[]},"session_ids":["mine"],"eval_set_id":"saved"}`
	req, status, msg := parseEvaluateRequest(jsonPost("/api/evaluate", body), store, archive)
	if msg != "" || req.evalSet == nil || req.evalSet.EvalSetID != "saved" {
		t.Fatalf("the saved set was not loaded: status %d msg %q", status, msg)
	}
	if _, status, _ = parseEvaluateRequest(jsonPost("/api/evaluate", strings.Replace(body, "saved", "gone", 1)), store, archive); status != http.StatusNotFound {
		t.Errorf("unknown saved set: status %d, want 404", status)
	}
	as := withRoleInfoForTest(scopedTo("a"))
	if _, status, _ = parseEvaluateRequest(as(jsonPost("/api/evaluate", body)), store, archive); status != http.StatusForbidden && status != http.StatusNotFound {
		t.Errorf("scoped role: status %d, want a refusal", status)
	}
}
