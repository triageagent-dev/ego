package mcptools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

const (
	defaultSpanLimit = 50
	maxSpanLimit     = 200
	// maxAttrChars clips one attribute value: a span can carry a whole prompt.
	maxAttrChars = 200
)

// SessionSpan is one span of get_session_spans. A span that did not fail carries its attributes only when they were
// asked for: a timeline of a long session is cheap, and the cause of a failure is always on the failed span.
type SessionSpan struct {
	Operation string `json:"operation"`
	// Depth is how far down the span tree the span is: 0 for a root.
	Depth int `json:"depth"`
	// AtMs is when the span began, in milliseconds after the session's first span.
	AtMs       int64   `json:"at_ms"`
	DurationMs float64 `json:"duration_ms"`
	Error      bool    `json:"error,omitempty"`
	// SpanID is given for a failed span, to refer to it.
	SpanID string `json:"span_id,omitempty"`
	// Path is the operations of a failed span's ancestors, root first, so the failure is placed in the work around it.
	Path []string `json:"path,omitempty"`
	// Message is what a failed span says about its failure: the status description, the exception or the error attribute.
	Message    string         `json:"message,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	// Parent holds the attributes of a failed span's parent: a failed run of a tool says which tool in its parent.
	Parent map[string]any `json:"parent,omitempty"`

	startUs int64
}

// SessionSpans is the result of get_session_spans.
type SessionSpans struct {
	SessionID string `json:"session_id"`
	// Started is when the session's first span began.
	Started   string        `json:"started,omitempty"`
	NumSpans  int           `json:"num_spans"`
	NumErrors int           `json:"num_errors"`
	Returned  int           `json:"returned"`
	Truncated bool          `json:"truncated,omitempty"`
	Spans     []SessionSpan `json:"spans"`
}

// SpanQuery narrows and shapes get_session_spans.
type SpanQuery struct {
	ErrorsOnly bool
	// Operation keeps the spans whose operation name contains it.
	Operation string
	// Attributes gives every returned span its attributes, not only the failed ones.
	Attributes bool
	Limit      int
}

// noiseAttr is true for attributes that say where a span ran or who sent it, not what it did.
func noiseAttr(k string) bool {
	switch k {
	case "service.name", "service.version", "terminal.type", "user.id", "session.id", "ego.tenant",
		"gen_ai.conversation.id", "gen_ai.agent.name", "gen_ai.provider.name", "gen_ai.system", "otel.status_code":
		return true
	}
	return strings.HasPrefix(k, "host.") || strings.HasPrefix(k, "os.") || strings.HasPrefix(k, "otel.scope.")
}

func clipAttr(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if r := []rune(s); len(r) > maxAttrChars {
		return string(r[:maxAttrChars]) + fmt.Sprintf("… (%d chars)", len(r))
	}
	return s
}

func attrsOf(sp *tracepkg.Span) map[string]any {
	out := map[string]any{}
	for k, v := range sp.Tags {
		if !noiseAttr(k) {
			out[k] = clipAttr(v)
		}
	}
	return out
}

func spanFailed(sp *tracepkg.Span) bool {
	return strings.EqualFold(fmt.Sprint(sp.Tags["otel.status_code"]), "ERROR")
}

// failureMessage is the first thing a failed span says about why, in the order senders usually say it.
func failureMessage(sp *tracepkg.Span) string {
	for _, k := range []string{"otel.status_description", "exception.message", "error.message", "error", "error_class", "exception.type", "error.type"} {
		if s, ok := sp.Tags[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// BuildSessionSpans lists the spans of a session's traces depth first, oldest trace first. The counts are of the whole
// session; q narrows and bounds what is returned.
func BuildSessionSpans(sessionID string, traces []*tracepkg.Trace, q SpanQuery) SessionSpans {
	if q.Limit < 1 {
		q.Limit = defaultSpanLimit
	}
	out := SessionSpans{SessionID: sessionID, Spans: []SessionSpan{}}
	var first int64
	var walk func(sp, parent *tracepkg.Span, path []string)
	walk = func(sp, parent *tracepkg.Span, path []string) {
		out.NumSpans++
		if out.NumSpans == 1 || sp.StartTime < first {
			first = sp.StartTime
		}
		failed := spanFailed(sp)
		if failed {
			out.NumErrors++
		}
		if (!q.ErrorsOnly || failed) && strings.Contains(sp.OperationName, q.Operation) {
			if len(out.Spans) < q.Limit {
				row := SessionSpan{
					Operation:  sp.OperationName,
					Depth:      len(path),
					DurationMs: float64(sp.Duration) / 1000,
					Error:      failed,
					startUs:    sp.StartTime,
				}
				if failed || q.Attributes {
					row.Attributes = attrsOf(sp)
				}
				if failed {
					row.SpanID = sp.SpanID
					row.Path = append([]string(nil), path...)
					row.Message = failureMessage(sp)
					if parent != nil {
						row.Parent = attrsOf(parent)
					}
				}
				out.Spans = append(out.Spans, row)
			} else {
				out.Truncated = true
			}
		}
		children := append([]*tracepkg.Span(nil), sp.Children...)
		sort.SliceStable(children, func(i, j int) bool { return children[i].StartTime < children[j].StartTime })
		for _, c := range children {
			walk(c, sp, append(path, sp.OperationName))
		}
	}
	for _, tr := range traces {
		roots := append([]*tracepkg.Span(nil), tr.RootSpans...)
		sort.SliceStable(roots, func(i, j int) bool { return roots[i].StartTime < roots[j].StartTime })
		for _, r := range roots {
			walk(r, nil, nil)
		}
	}
	for i := range out.Spans {
		out.Spans[i].AtMs = (out.Spans[i].startUs - first) / 1000
	}
	if out.NumSpans > 0 {
		out.Started = time.UnixMicro(first).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	out.Returned = len(out.Spans)
	return out
}
