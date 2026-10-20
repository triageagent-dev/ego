package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/triageagent-dev/ego/internal/otlp"
)

// multipartConvertRequest builds a POST /api/convert multipart request,
// mirroring multipartEvaluateRequest but without a "config" field (convert
// takes no evaluators).
func multipartConvertRequest(t *testing.T, filename string, traceContent []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("trace_files", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(traceContent); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/convert", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// TestConvertHandler_PopulatesMetadata is a regression test for the
// dashboard table's Name/Start Time/Conversation/Model columns spinning
// forever: before this file existed, POST /api/convert 404'd, so
// ui/src/context/TraceProvider.tsx's traceMetadata map was always empty.
func TestConvertHandler_PopulatesMetadata(t *testing.T) {
	tr := buildTrajectoryTrace()
	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}

	req := multipartConvertRequest(t, "trace_mysession.jsonl", content)
	rec := httptest.NewRecorder()

	convertHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data  convertTracesDataDTO `json:"data"`
		Error *string              `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v, body=%s", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", *resp.Error)
	}
	if len(resp.Data.Traces) != 1 {
		t.Fatalf("got %d trace entries, want 1", len(resp.Data.Traces))
	}

	entry := resp.Data.Traces[0]
	if entry.TraceID != "trace-1" {
		t.Errorf("TraceID = %q, want %q", entry.TraceID, "trace-1")
	}
	if entry.Metadata.AgentName == "" {
		t.Error("Metadata.AgentName is empty")
	}
	if entry.Metadata.Model != "gemini-live" {
		t.Errorf("Metadata.Model = %q, want %q", entry.Metadata.Model, "gemini-live")
	}
	if entry.Metadata.StartTime == 0 {
		t.Error("Metadata.StartTime = 0")
	}
	if entry.Metadata.UserInputPreview == "" {
		t.Error("Metadata.UserInputPreview is empty")
	}
	if entry.Metadata.SessionName != "mysession" {
		t.Errorf("Metadata.SessionName = %q, want %q", entry.Metadata.SessionName, "mysession")
	}
	if len(entry.Invocations) == 0 {
		t.Error("Invocations is empty")
	}
}

func TestConvertHandler_NoFiles(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/convert", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	convertHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
