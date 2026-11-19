package adk

import (
	"testing"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func llm(id, parent string, in, out int64) *tracepkg.Span {
	return &tracepkg.Span{TraceID: "t", SpanID: id, ParentSpanID: parent, Tags: map[string]any{
		GenAIRequestModel: "m", GenAIUsageInputTokens: in, GenAIUsageOutputTokens: out,
	}}
}

func plain(id, parent string) *tracepkg.Span {
	return &tracepkg.Span{TraceID: "t", SpanID: id, ParentSpanID: parent, Tags: map[string]any{}}
}

func countedIDs(spans ...*tracepkg.Span) map[string]bool {
	out := map[string]bool{}
	for sp := range CountedTokenSpans(spans) {
		out[sp.SpanID] = true
	}
	return out
}

// llm.chat wraps anthropic.messages.create and both report the same usage: it is one call.
func TestCountedTokenSpansWrapperAndSDKSpan(t *testing.T) {
	outer, inner := llm("a", "root", 20000, 2400), llm("b", "a", 20000, 2400)
	for name, order := range map[string][]*tracepkg.Span{
		"child first":  {inner, outer},
		"parent first": {outer, inner},
	} {
		got := countedIDs(order...)
		if len(got) != 1 || !got["b"] {
			t.Errorf("%s: counted %v, want only the inner span", name, got)
		}
	}
}

// The wrapper counts when nothing inside it reports usage, and an unrelated call counts on its own.
func TestCountedTokenSpansKeepsWhatIsNotNested(t *testing.T) {
	wrapper := llm("a", "root", 100, 10)
	bare := plain("b", "a")
	other := llm("c", "root", 50, 5)
	got := countedIDs(wrapper, bare, other)
	if !got["a"] || !got["c"] || len(got) != 2 {
		t.Errorf("counted %v, want a and c", got)
	}
}

// An intermediate span without usage does not break the cover, in any arrival order.
func TestCountedTokenSpansThroughASpanWithoutUsage(t *testing.T) {
	outer, mid, inner := llm("a", "root", 100, 10), plain("m", "a"), llm("b", "m", 100, 10)
	orders := [][]*tracepkg.Span{{outer, mid, inner}, {inner, mid, outer}, {inner, outer, mid}, {mid, outer, inner}}
	for i, order := range orders {
		if got := countedIDs(order...); len(got) != 1 || !got["b"] {
			t.Errorf("order %d: counted %v, want only b", i, got)
		}
	}
}

// Add reports the span that stops counting when the span inside it arrives later.
func TestTokenCoverDropsTheOuterSpanWhenTheInnerOneArrives(t *testing.T) {
	var c TokenCover
	if counts, dropped := c.Add(llm("a", "root", 10, 1)); !counts || len(dropped) != 0 {
		t.Fatalf("outer alone: counts %v dropped %v", counts, dropped)
	}
	counts, dropped := c.Add(llm("b", "a", 10, 1))
	if !counts || len(dropped) != 1 || dropped[0].SpanID != "a" {
		t.Errorf("inner arrives: counts %v dropped %v", counts, dropped)
	}
}

// A span delivered twice is one call.
func TestCountedTokenSpansIgnoresADuplicateDelivery(t *testing.T) {
	a := llm("a", "root", 10, 1)
	again := llm("a", "root", 10, 1)
	if got := CountedTokenSpans([]*tracepkg.Span{a, again}); len(got) != 1 || !got[a] {
		t.Errorf("counted %d spans, want the first copy only", len(got))
	}
}
