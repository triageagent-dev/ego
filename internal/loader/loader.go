// Package loader auto-detects the on-disk trace format (Jaeger JSON or OTLP JSON/JSONL) from file content and a
// filename hint, and dispatches to the right parser.
package loader

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

const (
	JaegerJSON = "jaeger-json"
	OTLPJSON   = "otlp-json"
)

// DetectFormat returns the trace format for content, using filename as a
//  1. A .jsonl extension always means OTLP (one span per line).
//  2. Otherwise the content is parsed as JSON and sniffed: a top-level
//     "resourceSpans" key means OTLP; a top-level "data" key means Jaeger.
//
// Returns "" when the format can't be determined.
func DetectFormat(filename string, content []byte) string {
	if strings.HasSuffix(strings.ToLower(filename), ".jsonl") {
		return OTLPJSON
	}

	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return ""
	}
	if _, ok := data["resourceSpans"]; ok {
		return OTLPJSON
	}
	if _, ok := data["data"]; ok {
		return JaegerJSON
	}
	return ""
}

// ParseFile loads traces from file content whose format is auto-detected from filename/content, or from an
// explicit format override when format != "".
func ParseFile(filename string, content []byte, format string) ([]*tracepkg.Trace, error) {
	if format == "" {
		format = DetectFormat(filename, content)
	}
	switch format {
	case OTLPJSON:
		return otlp.ParseOTLPContent(content)
	case JaegerJSON:
		return tracepkg.ParseJaegerJSON(content)
	default:
		return nil, fmt.Errorf(
			"could not detect trace format for %q: expected Jaeger JSON ({\"data\": [...]}) or OTLP JSON ({\"resourceSpans\": [...]}/.jsonl)",
			filename,
		)
	}
}
