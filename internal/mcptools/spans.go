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
	maxAttrChars = 300
)

// SessionSpan is one span of get_session_spans.
type SessionSpan struct {
	SpanID    string `json:"span_id"`
	Operation string `json:"operation"`
	// Path is the operations of the span's ancestors, root first, so a failed call is placed in the work around it.
	Path       []string `json:"path,omitempty"`
	Start      string   `json:"start"`
	DurationMs float64  `json:"duration_ms"`
	Error      bool     `json:"error"`
	// Message is what the span says about its failure: the status description, the exception or the error attribute.
	Message    string         `json:"message,omitempty"`
	Attributes map[string]any `json:"attributes"`
	// Parent holds the attributes of the span's parent, for a failed span only: a failed run of a tool says which tool
	// in its parent.
	Parent map[string]any `json:"parent,omitempty"`
}

// SessionSpans is the result of get_session_spans.
type SessionSpans struct {
	SessionID string        `json:"session_id"`
	NumSpans  int           `json:"num_spans"`
	NumErrors int           `json:"num_errors"`
	Returned  int           `json:"returned"`
	Truncated bool          `json:"truncated,omitempty"`
	Spans     []SessionSpan `json:"spans"`
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

// BuildSessionSpans lists the spans of a session's traces depth first, oldest trace first. errorsOnly keeps the failed
// spans; limit bounds what is returned, and the counts are of the whole session.
func BuildSessionSpans(sessionID string, traces []*tracepkg.Trace, errorsOnly bool, limit int) SessionSpans {
	if limit < 1 {
		limit = defaultSpanLimit
	}
	out := SessionSpans{SessionID: sessionID, Spans: []SessionSpan{}}
	var walk func(sp, parent *tracepkg.Span, path []string)
	walk = func(sp, parent *tracepkg.Span, path []string) {
		out.NumSpans++
		failed := spanFailed(sp)
		if failed {
			out.NumErrors++
		}
		if !errorsOnly || failed {
			if len(out.Spans) < limit {
				row := SessionSpan{
					SpanID:     sp.SpanID,
					Operation:  sp.OperationName,
					Path:       append([]string(nil), path...),
					Start:      time.UnixMicro(sp.StartTime).UTC().Format("2006-01-02T15:04:05.000Z"),
					DurationMs: float64(sp.Duration) / 1000,
					Error:      failed,
					Attributes: attrsOf(sp),
				}
				if failed {
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
	out.Returned = len(out.Spans)
	return out
}
