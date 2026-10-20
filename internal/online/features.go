package online

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Features are what the sampler knows about a completed session.
type Features struct {
	SessionID string
	TraceID   string
	// Kind is the call kind with the most tokens in the session: the LLM
	// span's gen_ai.agent.name, else its trace's root span name (the same
	// rule as the usage ledger).
	Kind    string
	Tenant  string
	Service string
	Version string
	Model   string
	// LLMCalls, Errors, Tokens and LatencyMs describe the whole session.
	LLMCalls    int
	Errors      int
	Tokens      int64
	LatencyMs   int64
	Fingerprint string
	EndMs       int64
	// ToolCalls counts the tool spans of the whole session. UserChars is the length of the first user message
	// and ReplyChars the length of the last model reply, in characters; either is 0 when it could not be read.
	ToolCalls  int
	UserChars  int
	ReplyChars int
}

var (
	digitsRe = regexp.MustCompile(`[0-9]+`)
	hexIDRe  = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// FeaturesOf reads a session's features from its spans. tenantAttr is the
// deployment's tenant attribute (--usage-tenant-attr); empty leaves Tenant
// blank. A session with no LLM span has no Kind and is never sampled.
func FeaturesOf(sessionID string, spans []*tracepkg.Span, tenantAttr string) Features {
	f := Features{SessionID: sessionID}
	roots := map[string]string{}
	var minStart, maxEnd int64
	for i, sp := range spans {
		if sp.ParentSpanID == "" {
			roots[sp.TraceID] = sp.OperationName
		}
		if i == 0 || sp.StartTime < minStart {
			minStart = sp.StartTime
		}
		if sp.EndTime() > maxEnd {
			maxEnd = sp.EndTime()
		}
		if sp.TagString("otel.status_code") == "ERROR" {
			f.Errors++
		}
	}
	f.LatencyMs = (maxEnd - minStart) / 1000
	f.EndMs = maxEnd / 1000

	type kindAgg struct {
		tokens int64
		span   *tracepkg.Span
		model  string
	}
	byKind := map[string]*kindAgg{}
	var firstLLM, lastLLM *tracepkg.Span
	var tools []string
	ordered := append([]*tracepkg.Span(nil), spans...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartTime < ordered[j].StartTime })
	for _, sp := range ordered {
		if adk.IsToolSpan(sp) {
			f.ToolCalls++
			name := sp.TagString(adk.GenAIToolName)
			if name == "" {
				name = sp.TagString(adk.OpenInferenceToolName)
			}
			if name != "" && (len(tools) == 0 || tools[len(tools)-1] != name) {
				tools = append(tools, name)
			}
		}
		if !adk.IsLLMSpan(sp) {
			continue
		}
		f.LLMCalls++
		lastLLM = sp
		in, out, model := adk.ExtractTokenUsageFromAttrs(sp.Tags)
		f.Tokens += in + out
		kind := sp.TagString(adk.GenAIAgentName)
		if kind == "" {
			kind = roots[sp.TraceID]
		}
		if kind == "" {
			continue
		}
		if firstLLM == nil {
			firstLLM = sp
		}
		a := byKind[kind]
		if a == nil {
			a = &kindAgg{span: sp, model: model}
			byKind[kind] = a
		}
		a.tokens += in + out
		if a.model == "" {
			a.model = model
		}
	}
	var best *kindAgg
	for kind, a := range byKind {
		if best == nil || a.tokens > best.tokens || (a.tokens == best.tokens && kind < f.Kind) {
			best, f.Kind = a, kind
		}
	}
	if best != nil {
		f.TraceID = best.span.TraceID
		f.Model = best.model
		f.Service = best.span.TagString(adk.OtelServiceName)
		f.Version = best.span.TagString("service.version")
		if tenantAttr != "" {
			f.Tenant = best.span.TagString(tenantAttr)
		}
	}
	f.Fingerprint = fingerprint(f.Kind, firstInput(firstLLM), tools)
	if firstLLM != nil {
		f.UserChars = utf8.RuneCountInString(strings.TrimSpace(stripWrappers(adk.ExtractUserTextFromAttrs(firstLLM.Tags))))
	}
	if lastLLM != nil {
		f.ReplyChars = utf8.RuneCountInString(strings.TrimSpace(adk.ExtractAgentResponseFromAttrs(lastLLM.Tags)))
	}
	return f
}

// wrapperLineRe matches a leading line that is only a bracketed note, such as the timestamp or the screen context a
// chat front end prepends to what the person typed.
var wrapperLineRe = regexp.MustCompile(`^\s*\[[^\]\n]*\][ \t]*\n`)

// stripWrappers drops those leading bracketed lines, so "[now ...]\n[context ...]\nbye" reads as "bye".
func stripWrappers(s string) string {
	for {
		loc := wrapperLineRe.FindStringIndex(s)
		if loc == nil {
			return s
		}
		s = s[loc[1]:]
	}
}

// NotWorthJudging reports whether the session is too slight to score: no tool was called, nothing failed, and both
// what was asked and what came back are short, like a greeting or a goodbye. Judging it costs tokens and says nothing, and the
// scores would pull a kind's baseline up or down for no reason. A session whose text could not be read is never
// called trivial.
func (f Features) NotWorthJudging(t Trivial) bool {
	if t.Disabled || f.ToolCalls > 0 || f.Errors > 0 || f.UserChars == 0 || f.ReplyChars == 0 {
		return false
	}
	return f.UserChars <= t.MaxUserChars && f.ReplyChars <= t.MaxReplyChars
}

// firstInput is the first LLM call's raw input text.
func firstInput(sp *tracepkg.Span) string {
	if sp == nil {
		return ""
	}
	for _, key := range []string{adk.GenAIInputMessages, adk.OpenInferenceInputValue} {
		switch v := sp.Tag(key).(type) {
		case string:
			if v != "" {
				return v
			}
		case nil:
		default:
			return fmt.Sprint(v)
		}
	}
	return ""
}

// fingerprint hashes the kind, the normalised first input (numbers and hex
// IDs collapsed, so timestamps and counters do not make every session
// unique) and the tool-call sequence.
func fingerprint(kind, input string, tools []string) string {
	norm := strings.ToLower(input)
	if len(norm) > 600 {
		norm = norm[:600]
	}
	norm = hexIDRe.ReplaceAllString(norm, "#")
	norm = digitsRe.ReplaceAllString(norm, "#")
	norm = spaceRe.ReplaceAllString(norm, " ")
	h := sha256.Sum256([]byte(kind + "\x00" + norm + "\x00" + strings.Join(tools, ">")))
	return hex.EncodeToString(h[:8])
}
