package loader_test

import (
	"testing"

	"github.com/triageagent-dev/ego/internal/loader"
)

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		content  string
		want     string
	}{
		{"jsonl extension", "trace.jsonl", `{"traceId":"t1"}`, loader.OTLPJSON},
		{"JSONL extension case-insensitive", "trace.JSONL", `{"traceId":"t1"}`, loader.OTLPJSON},
		{"otlp resourceSpans", "trace.json", `{"resourceSpans":[]}`, loader.OTLPJSON},
		{"jaeger data", "trace.json", `{"data":[]}`, loader.JaegerJSON},
		{"unrecognized", "trace.json", `{"foo":[]}`, ""},
		{"invalid json", "trace.json", `not json`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := loader.DetectFormat(c.filename, []byte(c.content))
			if got != c.want {
				t.Errorf("DetectFormat(%q, %q) = %q, want %q", c.filename, c.content, got, c.want)
			}
		})
	}
}

func TestParseFile_Jaeger(t *testing.T) {
	content := []byte(`{"data":[{"traceID":"t1","spans":[{"traceID":"t1","spanID":"s1","operationName":"op","startTime":1000,"duration":500,"tags":[]}]}]}`)
	traces, err := loader.ParseFile("trace.json", content, "")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(traces) != 1 || len(traces[0].AllSpans) != 1 {
		t.Fatalf("expected 1 trace with 1 span, got %+v", traces)
	}
}

func TestParseFile_OTLPJSONL(t *testing.T) {
	content := []byte(`{"traceId":"t1","spanId":"s1","name":"op","startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000","attributes":[]}`)
	traces, err := loader.ParseFile("trace.jsonl", content, "")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(traces) != 1 || len(traces[0].AllSpans) != 1 {
		t.Fatalf("expected 1 trace with 1 span, got %+v", traces)
	}
}

func TestParseFile_Undetected(t *testing.T) {
	if _, err := loader.ParseFile("trace.json", []byte(`{"foo":1}`), ""); err == nil {
		t.Fatal("expected an error for an undetectable format")
	}
}
