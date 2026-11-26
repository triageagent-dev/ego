package online

import (
	"strings"
	"testing"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func asked(system, user string) []*tracepkg.Span {
	return []*tracepkg.Span{{TraceID: "t", SpanID: "s", OperationName: "llm.chat.completions", Tags: map[string]any{
		"openinference.span.kind": "LLM", "gen_ai.request.model": "m", "gen_ai.agent.name": "reflection",
		"gen_ai.input.messages": `[{"role":"system","content":"` + system + `"},{"role":"user","content":"` + user + `"}]`,
	}}}
}

// The duplicate check reads what was asked, not the system prompt in front of it: a long shared system prompt must
// not make two different questions one.
func TestFingerprint_ReadsTheUserMessage(t *testing.T) {
	system := strings.Repeat("You are the memory agent for this platform. ", 40)
	of := func(user string) string { return FeaturesOf("id", asked(system, user), "").Fingerprint }
	if of("checkout is down") == of("the auth service was OOM killed") {
		t.Error("two different questions behind one system prompt have the same fingerprint")
	}
	if of("auth was killed 3 times") != of("auth was killed 7 times") {
		t.Error("questions that differ only by numbers must still match")
	}
}

// One tenant's session never makes another tenant's a repeat of it.
func TestSampler_DuplicatesAreWithinATenant(t *testing.T) {
	s := NewSampler(Sampling{DedupWindow: Duration(6 * time.Hour)})
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	f := func(tenant string) Features { return Features{Tenant: tenant, Kind: "reflection", Fingerprint: "same"} }
	if d := s.DecideAt(f("a"), 10, now); !d.Sample {
		t.Fatalf("first of tenant a: %+v", d)
	}
	if d := s.DecideAt(f("b"), 10, now.Add(time.Minute)); !d.Sample {
		t.Errorf("tenant b was skipped for tenant a's session: %+v", d)
	}
	if d := s.DecideAt(f("a"), 10, now.Add(2*time.Minute)); d.Skip != SkipDuplicate {
		t.Errorf("a repeat within tenant a: %+v, want a duplicate", d)
	}
}
