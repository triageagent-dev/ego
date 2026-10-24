package api

import (
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/loader"
	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// A session the server already holds is named by its id instead of being uploaded. "Continue to evaluation" used
// to download each trace and post the same text back, which is slow and which a WAF in front of the server can
// read as an attack: every multipart part carries a Content-Type line after a line break, the shape of a response
// splitting attempt. POST /api/convert and POST /api/evaluate accept a JSON body for it; uploads still work.

// maxSessionRefs bounds how many sessions one request may name.
const maxSessionRefs = 200

// sessionRefs is the JSON body of POST /api/convert.
type sessionRefs struct {
	SessionIDs []string `json:"session_ids"`
}

// evaluateByIDRequest is the JSON body of POST /api/evaluate and /api/evaluate/stream: the multipart request's
// config, with the traces and the golden eval set named by session id.
type evaluateByIDRequest struct {
	Config           evalConfigDTO `json:"config"`
	SessionIDs       []string      `json:"session_ids"`
	EvalSetSessionID string        `json:"eval_set_session_id,omitempty"`
	// TraceIDs limits the evaluation to these traces of the named sessions (the turns the reader picked); empty
	// means every trace.
	TraceIDs []string `json:"trace_ids,omitempty"`
}

// keepTraces drops every loaded trace whose id is not in ids; no ids keeps them all.
func keepTraces(req *evaluateRequest, ids []string) {
	if len(ids) == 0 {
		return
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	kept := req.traces[:0:0]
	for _, t := range req.traces {
		if want[t.TraceID] {
			kept = append(kept, t)
		}
	}
	req.traces = kept
}

// traceInput is one trace file's name and text, uploaded or encoded from a stored session.
type traceInput struct {
	Filename string
	Content  []byte
}

func isJSONBody(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/json"
}

// sessionTraceInputs encodes the stored traces of the sessions in ids as OTLP JSONL, named as the UI named the
// files it used to upload. A session the caller may not see is answered as not found, as get-trace does.
func sessionTraceInputs(store *SessionStore, r *http.Request, ids []string) ([]traceInput, string) {
	if len(ids) == 0 {
		return nil, "No sessions provided"
	}
	if len(ids) > maxSessionRefs {
		return nil, fmt.Sprintf("too many sessions: %d (at most %d per request)", len(ids), maxSessionRefs)
	}
	if store == nil {
		return nil, "sessions are not available"
	}
	inputs := make([]traceInput, 0, len(ids))
	for _, id := range ids {
		tr, ok := store.Trace(id)
		if !ok || !sessionInScope(store, r, id) {
			return nil, fmt.Sprintf("session %q not found", id)
		}
		content, err := otlp.EncodeTraceJSONL(tr)
		if err != nil {
			return nil, fmt.Sprintf("encoding session %q: %v", id, err)
		}
		inputs = append(inputs, traceInput{Filename: "trace_" + id + ".jsonl", Content: content})
	}
	return inputs, ""
}

// goldenEvalSet is the eval set whose one case is the session's own conversation, the golden reference the
// other sessions are scored against. detail is set when the trace has no conversation to take.
func goldenEvalSet(tr *tracepkg.Trace, evalSetID string) (set adk.EvalSet, detail string) {
	result := adk.ConvertTrace(tr)
	if len(result.Invocations) == 0 {
		detail = "Failed to convert trace"
		if len(result.Warnings) > 0 {
			detail = strings.Join(result.Warnings, "; ")
		}
		return adk.EvalSet{}, detail
	}
	return adk.EvalSet{
		EvalSetID: evalSetID,
		EvalCases: []adk.EvalCase{{EvalID: "case_1", Conversation: result.Invocations}},
	}, ""
}

// parseEvaluateByID is parseEvaluateRequest for a JSON body naming sessions. It returns the same request, so
// everything after parsing is shared with uploads.
func parseEvaluateByID(r *http.Request, sessions *SessionStore) (*evaluateRequest, int, string) {
	var body evaluateByIDRequest
	if err := decodeJSON(nil, r, &body); err != nil {
		return nil, http.StatusBadRequest, "invalid request body: " + err.Error()
	}
	if msg := validateEvalConfig(body.Config, len(body.SessionIDs)); msg != "" {
		return nil, http.StatusBadRequest, msg
	}
	inputs, msg := sessionTraceInputs(sessions, r, body.SessionIDs)
	if msg != "" {
		status := http.StatusBadRequest
		if strings.HasSuffix(msg, "not found") {
			status = http.StatusNotFound
		}
		return nil, status, msg
	}

	req := &evaluateRequest{cfg: body.Config, traceFilenames: map[string]string{}}
	seen := map[string]bool{}
	for _, id := range body.SessionIDs {
		if sum, ok := sessions.Summary(id); ok {
			for _, k := range sum.Tenants {
				if !seen[k] {
					seen[k] = true
					req.tenants = append(req.tenants, k)
				}
			}
		}
	}
	for _, in := range inputs {
		parsed, err := loader.ParseFile(in.Filename, in.Content, "")
		if err != nil {
			req.loadErrors = append(req.loadErrors, fmt.Sprintf("Failed to load trace file %q: %s", in.Filename, err))
			continue
		}
		for _, t := range parsed {
			req.traceFilenames[t.TraceID] = in.Filename
		}
		req.traces = append(req.traces, parsed...)
	}
	keepTraces(req, body.TraceIDs)

	if id := body.EvalSetSessionID; id != "" {
		tr, ok := sessions.Trace(id)
		if !ok || !sessionInScope(sessions, r, id) {
			return nil, http.StatusNotFound, fmt.Sprintf("session %q not found", id)
		}
		set, detail := goldenEvalSet(tr, "golden_"+id)
		if detail != "" {
			req.loadErrors = append(req.loadErrors, "Failed to build the eval set: "+detail)
		} else {
			req.evalSet = &set
		}
	}
	return req, 0, ""
}
