package online

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

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
	var firstLLM *tracepkg.Span
	var tools []string
	ordered := append([]*tracepkg.Span(nil), spans...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartTime < ordered[j].StartTime })
	for _, sp := range ordered {
		if adk.IsToolSpan(sp) {
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
	return f
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
