package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// LoadJaegerJSON loads traces from a Jaeger JSON export file:
//
//	{"data": [{"traceID": "...", "spans": [{"traceID", "spanID", "operationName",
//	  "references": [{"refType": "CHILD_OF", "spanID": "..."}], "startTime",
//	  "duration", "tags": [{"key", "value"}]}]}]}
func LoadJaegerJSON(path string) ([]*Trace, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseJaegerJSON(raw)
}

// ParseJaegerJSON parses Jaeger JSON export content already read into
// memory (e.g. an uploaded file), the same format LoadJaegerJSON reads
// from disk.
func ParseJaegerJSON(raw []byte) ([]*Trace, error) {
	var file jaegerFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("invalid Jaeger JSON format: expected top-level 'data' key: %w", err)
	}
	if file.Data == nil {
		return nil, fmt.Errorf("invalid Jaeger JSON format: expected top-level 'data' key")
	}

	traces := make([]*Trace, 0, len(file.Data))
	for _, td := range file.Data {
		if t := parseJaegerTrace(td); t != nil {
			traces = append(traces, t)
		}
	}
	return traces, nil
}

type jaegerFile struct {
	Data []jaegerTrace `json:"data"`
}

type jaegerTrace struct {
	TraceID string       `json:"traceID"`
	Spans   []jaegerSpan `json:"spans"`
}

type jaegerSpan struct {
	TraceID       string      `json:"traceID"`
	SpanID        string      `json:"spanID"`
	OperationName string      `json:"operationName"`
	References    []jaegerRef `json:"references"`
	StartTime     int64       `json:"startTime"`
	Duration      int64       `json:"duration"`
	Tags          []jaegerTag `json:"tags"`
}

type jaegerRef struct {
	RefType string `json:"refType"`
	SpanID  string `json:"spanID"`
}

type jaegerTag struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

func parseJaegerTrace(td jaegerTrace) *Trace {
	if len(td.Spans) == 0 {
		return nil
	}

	byID := make(map[string]*Span, len(td.Spans))
	for _, raw := range td.Spans {
		byID[raw.SpanID] = parseJaegerSpan(raw)
	}

	var roots []*Span
	for _, raw := range td.Spans {
		span := byID[raw.SpanID]
		if span.ParentSpanID != "" {
			if parent, ok := byID[span.ParentSpanID]; ok {
				parent.Children = append(parent.Children, span)
				continue
			}
		}
		roots = append(roots, span)
	}

	for _, span := range byID {
		sortSpansByStart(span.Children)
	}
	sortSpansByStart(roots)

	all := make([]*Span, 0, len(byID))
	for _, raw := range td.Spans {
		all = append(all, byID[raw.SpanID])
	}

	return &Trace{TraceID: td.TraceID, RootSpans: roots, AllSpans: all}
}

func parseJaegerSpan(raw jaegerSpan) *Span {
	tags := make(map[string]any, len(raw.Tags))
	for _, t := range raw.Tags {
		tags[t.Key] = t.Value
	}
	return &Span{
		TraceID:       raw.TraceID,
		SpanID:        raw.SpanID,
		ParentSpanID:  jaegerParentSpanID(raw),
		OperationName: raw.OperationName,
		StartTime:     raw.StartTime,
		Duration:      raw.Duration,
		Tags:          tags,
	}
}

func jaegerParentSpanID(raw jaegerSpan) string {
	for _, ref := range raw.References {
		if ref.RefType == "CHILD_OF" {
			return ref.SpanID
		}
	}
	return ""
}

func sortSpansByStart(spans []*Span) {
	sort.Slice(spans, func(i, j int) bool { return spans[i].StartTime < spans[j].StartTime })
}
