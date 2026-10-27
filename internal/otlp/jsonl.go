package otlp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// ParseJSONLSpans parses OTLP JSONL content - one flat span dict per line, each already carrying its own
// traceId (no separate resourceSpans/ scopeSpans envelope) - into normalized Traces, grouping by trace ID.
func ParseJSONLSpans(content []byte) ([]*tracepkg.Trace, error) {
	var spans []*tracepkg.Span
	for i, line := range bytes.Split(content, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var spanData map[string]any
		if err := json.Unmarshal(line, &spanData); err != nil {
			return nil, fmt.Errorf("parsing OTLP JSONL line %d: %w", i+1, err)
		}
		spans = append(spans, parseSpan(spanData, nil, "", "", "", nil))
	}
	return buildTraces(spans), nil
}

// isOTLPExport reports whether data looks like a standard OTLP/JSON export ({"resourceSpans": [...]}).
func isOTLPExport(data map[string]any) bool {
	_, ok := data["resourceSpans"]
	return ok
}

// ParseOTLPContent parses OTLP/JSON trace file content, accepting either a full export document
// ({"resourceSpans": [...]}) or JSONL (one flat span per line).
func ParseOTLPContent(content []byte) ([]*tracepkg.Trace, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '{' {
		var data map[string]any
		if err := json.Unmarshal(trimmed, &data); err == nil && isOTLPExport(data) {
			return ParseExportRequest(data), nil
		}
	}
	return ParseJSONLSpans(trimmed)
}

// EncodeSpanJSONL renders one normalized Span back as a flat OTLP/JSON span dict (the JSONL line shape
// ParseJSONLSpans reads), so a live session's spans can be handed to the UI/re-uploaded to /api/evaluate
// without losing attributes..
func EncodeSpanJSONL(s *tracepkg.Span) map[string]any {
	startNs := s.StartTime * 1000
	endNs := (s.StartTime + s.Duration) * 1000
	m := map[string]any{
		"traceId":           s.TraceID,
		"spanId":            s.SpanID,
		"name":              s.OperationName,
		"startTimeUnixNano": strconv.FormatInt(startNs, 10),
		"endTimeUnixNano":   strconv.FormatInt(endNs, 10),
		"attributes":        EncodeAttributes(s.Tags),
	}
	if s.ParentSpanID != "" {
		m["parentSpanId"] = s.ParentSpanID
	}
	return m
}

// EncodeTraceJSONL renders every span in tr as OTLP JSONL (one JSON object
// per line, newline-terminated), the format the UI expects from
// POST /api/streaming/get-trace and re-uploads to POST /api/evaluate.
func EncodeTraceJSONL(tr *tracepkg.Trace) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, s := range tr.AllSpans {
		if err := enc.Encode(EncodeSpanJSONL(s)); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
