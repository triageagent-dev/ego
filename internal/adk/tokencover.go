package adk

import (
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// A call to a model is often traced twice: the program wraps it in a span of its own (llm.chat) and the provider's
// SDK adds the span of the request (anthropic.messages.create) under it, and both carry the usage. Adding up every
// LLM span counts those tokens twice. TokenCover decides which spans' tokens count: a span that has an LLM span
// with tokens under it is covered by it, since the inner span is the call that was made; the outer one counts only
// when nothing inside it reports usage.

// HasTokenUsage reports whether span is an LLM span that reports tokens.
func HasTokenUsage(span *tracepkg.Span) bool {
	if !isLLMSpan(span) {
		return false
	}
	in, out, _ := ExtractTokenUsageFromAttrs(span.Tags)
	return in > 0 || out > 0
}

func coverKey(traceID, spanID string) string { return traceID + "/" + spanID }

// TokenCover works out, as spans arrive in any order, which LLM spans' tokens count. The zero value is ready to use.
type TokenCover struct {
	parent  map[string]string         // span -> its parent, for the spans seen
	counted map[string]*tracepkg.Span // the spans whose tokens count so far
	covered map[string]bool           // the spans that have a counted span under them
}

// Add takes in the next span. counts says whether its tokens count, and dropped lists spans that counted until now
// and no longer do, because this span is inside them.
func (c *TokenCover) Add(span *tracepkg.Span) (counts bool, dropped []*tracepkg.Span) {
	if span.SpanID == "" {
		// A span with no id cannot be placed in a tree, nor told from another: it counts as it is.
		return HasTokenUsage(span), nil
	}
	if c.parent == nil {
		c.parent, c.counted, c.covered = map[string]string{}, map[string]*tracepkg.Span{}, map[string]bool{}
	}
	key := coverKey(span.TraceID, span.SpanID)
	// A span delivered twice (a retried export) is the same call.
	if _, seen := c.counted[key]; seen {
		return false, nil
	}
	up := ""
	if span.ParentSpanID != "" {
		up = coverKey(span.TraceID, span.ParentSpanID)
		c.parent[key] = up
	}
	if HasTokenUsage(span) && !c.covered[key] {
		c.counted[key] = span
		counts = true
	}
	// Whatever is above a counted span, or above a span that is covered, is covered too. The walk stops at a span
	// that has not arrived yet; when it does, it is covered and passes the cover up from there.
	if counts || c.covered[key] {
		dropped = c.cover(up)
	}
	return counts, dropped
}

func (c *TokenCover) cover(key string) (dropped []*tracepkg.Span) {
	for key != "" && !c.covered[key] {
		c.covered[key] = true
		if sp, ok := c.counted[key]; ok {
			delete(c.counted, key)
			dropped = append(dropped, sp)
		}
		key = c.parent[key]
	}
	return dropped
}

// CountedTokenSpans returns the spans of spans whose tokens count: the LLM spans that report usage and have no LLM
// span with usage inside them.
func CountedTokenSpans(spans []*tracepkg.Span) map[*tracepkg.Span]bool {
	var c TokenCover
	out := make(map[*tracepkg.Span]bool)
	for _, sp := range spans {
		if counts, _ := c.Add(sp); counts && sp.SpanID == "" {
			out[sp] = true
		}
	}
	for _, sp := range c.counted {
		out[sp] = true
	}
	return out
}
