package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// buildTrajectoryTrace reproduces triage-core's OpenInference span shape
// (see internal/adk/genai_converter_test.go's buildOpenInferenceTrace),
// which adk.ConvertTrace auto-detects and converts into one invocation
// with a single get_time tool call.
func buildTrajectoryTrace() *tracepkg.Trace {
	root := &tracepkg.Span{
		TraceID:       "trace-1",
		SpanID:        "root",
		OperationName: "voice.session",
		StartTime:     1_000_000,
		Duration:      9_000_000,
		Tags:          map[string]any{},
	}
	userSpan := &tracepkg.Span{
		TraceID: "trace-1", SpanID: "s1", ParentSpanID: "root",
		OperationName: "llm.gemini_realtime",
		StartTime:     1_000_000, Duration: 100_000,
		Tags: map[string]any{"input.value": "what time is it in tokyo", "gen_ai.request.model": "gemini-live"},
	}
	toolSpan := &tracepkg.Span{
		TraceID: "trace-1", SpanID: "s2", ParentSpanID: "root",
		OperationName: "tool.get_time",
		StartTime:     1_200_000, Duration: 50_000,
		Tags: map[string]any{
			"tool.name":    "get_time",
			"input.value":  `{"tz": "Asia/Tokyo"}`,
			"output.value": `{"time": "10:00"}`,
		},
	}
	assistantSpan := &tracepkg.Span{
		TraceID: "trace-1", SpanID: "s3", ParentSpanID: "root",
		OperationName: "llm.gemini_realtime",
		StartTime:     1_500_000, Duration: 100_000,
		Tags: map[string]any{"output.value": "it is 10am in tokyo", "gen_ai.request.model": "gemini-live"},
	}
	root.Children = []*tracepkg.Span{userSpan, toolSpan, assistantSpan}

	return &tracepkg.Trace{
		TraceID:   "trace-1",
		RootSpans: []*tracepkg.Span{root},
		AllSpans:  []*tracepkg.Span{root, userSpan, toolSpan, assistantSpan},
	}
}

func multipartEvaluateRequest(t *testing.T, traceContent []byte, evalSetContent []byte, config string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("trace_files", "trace.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(traceContent); err != nil {
		t.Fatal(err)
	}

	if evalSetContent != nil {
		esPart, err := w.CreateFormFile("eval_set_file", "eval_set.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := esPart.Write(evalSetContent); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.WriteField("config", config); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/evaluate", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestEvaluateHandler_NoEvalSet(t *testing.T) {
	tr := buildTrajectoryTrace()
	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}

	config := `{"evaluators":[{"type":"builtin","name":"tool_trajectory_avg_score"}]}`
	req := multipartEvaluateRequest(t, content, nil, config)
	rec := httptest.NewRecorder()

	evaluateHandler(nil, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data  runResultDTO `json:"data"`
		Error *string      `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("unexpected error: %s", *envelope.Error)
	}
	if len(envelope.Data.TraceResults) != 1 {
		t.Fatalf("expected 1 trace result, got %d", len(envelope.Data.TraceResults))
	}
	tr0 := envelope.Data.TraceResults[0]
	if tr0.NumInvocations != 1 {
		t.Errorf("NumInvocations = %d, want 1", tr0.NumInvocations)
	}
	if len(tr0.MetricResults) != 1 {
		t.Fatalf("expected 1 metric result, got %d", len(tr0.MetricResults))
	}
	mr := tr0.MetricResults[0]
	if mr.Error == nil {
		t.Fatal("expected an error result since no eval set/expected invocations were provided")
	}
	if mr.EvalStatus != "ERROR" {
		t.Errorf("EvalStatus = %q, want ERROR", mr.EvalStatus)
	}
}

func TestEvaluateHandler_WithEvalSet_Passes(t *testing.T) {
	tr := buildTrajectoryTrace()
	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}

	evalSet := `{
		"eval_set_id": "set-1",
		"eval_cases": [{
			"eval_id": "case_1",
			"conversation": [{
				"invocation_id": "inv-1",
				"user_content": {"role": "user", "parts": [{"text": "what time is it in tokyo"}]},
				"final_response": {"role": "model", "parts": [{"text": "it is 10am in tokyo"}]},
				"intermediate_data": {
					"tool_uses": [{"name": "get_time", "args": {"tz": "Asia/Tokyo"}}],
					"tool_responses": [],
					"intermediate_responses": []
				}
			}]
		}]
	}`

	config := `{"evaluators":[{"type":"builtin","name":"tool_trajectory_avg_score","threshold":0.5}]}`
	req := multipartEvaluateRequest(t, content, []byte(evalSet), config)
	rec := httptest.NewRecorder()

	evaluateHandler(nil, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data  runResultDTO `json:"data"`
		Error *string      `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("unexpected error: %s", *envelope.Error)
	}
	if len(envelope.Data.TraceResults) != 1 || len(envelope.Data.TraceResults[0].MetricResults) != 1 {
		t.Fatalf("unexpected shape: %+v", envelope.Data)
	}
	mr := envelope.Data.TraceResults[0].MetricResults[0]
	if mr.Error != nil {
		t.Fatalf("unexpected metric error: %s", *mr.Error)
	}
	if mr.EvalStatus != "PASSED" {
		t.Errorf("EvalStatus = %q, want PASSED (score=%v)", mr.EvalStatus, mr.Score)
	}
	if mr.EvaluatorKind != "algorithm" {
		t.Errorf("EvaluatorKind = %q, want %q (tool_trajectory_avg_score is deterministic, not judge-model-based)", mr.EvaluatorKind, "algorithm")
	}
	if mr.JudgeModel != "" {
		t.Errorf("JudgeModel = %q, want empty for an algorithm-kind metric", mr.JudgeModel)
	}
}

func TestEvaluateHandler_NoTraceFiles(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("config", `{"evaluators":[]}`)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/evaluate", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	evaluateHandler(nil, nil)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestEvaluateStreamHandler_WithEvalSet_Passes(t *testing.T) {
	tr := buildTrajectoryTrace()
	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}

	evalSet := `{
		"eval_set_id": "set-1",
		"eval_cases": [{
			"eval_id": "case_1",
			"conversation": [{
				"invocation_id": "inv-1",
				"user_content": {"role": "user", "parts": [{"text": "what time is it in tokyo"}]},
				"final_response": {"role": "model", "parts": [{"text": "it is 10am in tokyo"}]},
				"intermediate_data": {
					"tool_uses": [{"name": "get_time", "args": {"tz": "Asia/Tokyo"}}],
					"tool_responses": [],
					"intermediate_responses": []
				}
			}]
		}]
	}`

	config := `{"evaluators":[{"type":"builtin","name":"tool_trajectory_avg_score","threshold":0.5}]}`
	req := multipartEvaluateRequest(t, content, []byte(evalSet), config)
	req.URL.Path = "/api/evaluate/stream"
	rec := httptest.NewRecorder()

	evaluateStreamHandler(nil, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	events := parseSSEEvents(t, rec.Body.Bytes())

	var progressCount, traceProgressCount int
	var done map[string]any
	for _, ev := range events {
		switch {
		case ev["message"] != nil:
			progressCount++
		case ev["traceProgress"] != nil:
			traceProgressCount++
		case ev["done"] != nil:
			done = ev
		case ev["error"] != nil:
			t.Fatalf("unexpected error event: %v", ev["error"])
		}
	}
	if progressCount == 0 {
		t.Error("expected at least one progress message event")
	}
	if traceProgressCount == 0 {
		t.Error("expected at least one traceProgress event")
	}
	if done == nil {
		t.Fatal("expected a done event")
	}

	resultRaw, err := json.Marshal(done["result"])
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var result runResultDTO
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.TraceResults) != 1 || len(result.TraceResults[0].MetricResults) != 1 {
		t.Fatalf("unexpected shape: %+v", result)
	}
	mr := result.TraceResults[0].MetricResults[0]
	if mr.Error != nil {
		t.Fatalf("unexpected metric error: %s", *mr.Error)
	}
	if mr.EvalStatus != "PASSED" {
		t.Errorf("EvalStatus = %q, want PASSED", mr.EvalStatus)
	}
}

func TestEvaluateStreamHandler_NoTraceFiles(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("config", `{"evaluators":[]}`)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/evaluate/stream", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	evaluateStreamHandler(nil, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (errors are reported as SSE events)", rec.Code)
	}
	events := parseSSEEvents(t, rec.Body.Bytes())
	if len(events) != 1 || events[0]["error"] == nil {
		t.Fatalf("expected a single error event, got %+v", events)
	}
}

// parseSSEEvents splits a recorded SSE response body on blank lines and
// JSON-decodes each "data: " line, skipping ": ping" keep-alive comments.
func parseSSEEvents(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, block := range bytes.Split(body, []byte("\n\n")) {
		block = bytes.TrimSpace(block)
		if len(block) == 0 {
			continue
		}
		for _, line := range bytes.Split(block, []byte("\n")) {
			line = bytes.TrimSpace(line)
			data, ok := bytes.CutPrefix(line, []byte("data: "))
			if !ok {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				t.Fatalf("unmarshal SSE data line %q: %v", data, err)
			}
			events = append(events, ev)
		}
	}
	return events
}
