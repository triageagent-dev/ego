// Package online is the continuous evaluation loop: it samples completed
// sessions, budgets judge spend, detects score regressions and formats
// notifications. Unlike a one-off run over
// uploaded traces, it works on live sessions. The package holds the decisions only; internal/api wires it to
// the session store, the evaluators and SQLite. See docs/online-evaluation.md.
package online

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Evaluator is one evaluator of a profile, in the POST /api/evaluate
// evaluator shape so every metric family works unchanged.
type Evaluator struct {
	Type                string          `json:"type,omitempty"`
	Name                string          `json:"name"`
	JudgeModel          string          `json:"judgeModel,omitempty"`
	Threshold           *float64        `json:"threshold,omitempty"`
	TrajectoryMatchType string          `json:"trajectoryMatchType,omitempty"`
	Rubrics             []string        `json:"rubrics,omitempty"`
	Questions           json.RawMessage `json:"questions,omitempty"`
	// NumSamples is the judge samples per invocation; 0 means 1 here.
	NumSamples int `json:"numSamples,omitempty"`
}

// Profile maps sessions of one call kind (optionally narrowed to a service
// or tenant) to the evaluators that score them.
type Profile struct {
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Tenant  string `json:"tenant,omitempty"`
	// MaxInvocations scores only the last N invocations (default 3).
	MaxInvocations int         `json:"maxInvocations,omitempty"`
	Evaluators     []Evaluator `json:"evaluators"`
}

// Key names the profile in counters and cost estimates.
func (p Profile) Key() string {
	k := p.Kind
	if p.Service != "" {
		k += "@" + p.Service
	}
	if p.Tenant != "" {
		k += "#" + p.Tenant
	}
	return k
}

// Budget caps judge spend per UTC day. Zero disables that family.
type Budget struct {
	JudgeTokensPerDay int64 `json:"judgeTokensPerDay"`
	JevCallsPerDay    int64 `json:"jevCallsPerDay"`
}

// Sampling sets the per-(tenant, kind) rates and the dedup window.
type Sampling struct {
	PerKindPerHour      float64  `json:"perKindPerHour"`
	BoostPerKindPerHour float64  `json:"boostPerKindPerHour"`
	AfterChangeSessions int      `json:"afterChangeSessions"`
	DedupWindow         Duration `json:"dedupWindow"`
	// Trivial keeps greetings and the like out of the sample; see Features.NotWorthJudging.
	Trivial Trivial `json:"trivial"`
}

// Trivial is the "not worth judging" rule: a session with no tool calls whose first user message is at most
// MaxUserChars and whose last reply is at most MaxReplyChars is not sampled. Zero values take the defaults (40 and
// 100); Disabled turns the rule off.
type Trivial struct {
	Disabled      bool `json:"disabled,omitempty"`
	MaxUserChars  int  `json:"maxUserChars,omitempty"`
	MaxReplyChars int  `json:"maxReplyChars,omitempty"`
}

// Detector sets the regression test.
type Detector struct {
	Window     Duration `json:"window"`
	Baseline   Duration `json:"baseline"`
	MinSamples int      `json:"minSamples"`
	MinDrop    float64  `json:"minDrop"`
	Every      Duration `json:"every"`
}

// Findings configures the findings pass and the verification of corrections (docs/rsi.md, sections 2 and 3).
// MinAgreement and RecalibrateEvery belong to the label gate and anchor re-scoring; they are parsed and checked
// now so a config written for the later phase loads, and used there.
type Findings struct {
	// Enabled false writes no findings; corrections can still be registered and verified.
	Enabled bool `json:"enabled"`
	// Every is the sweep interval over series that have always been bad.
	Every Duration `json:"every"`
	// Window is the span of scores a finding is written from (and the time a series must stay healthy to go stale).
	Window Duration `json:"window"`
	// MinSessions is how many sessions a series needs in the window before the sweep may judge it.
	MinSessions int `json:"minSessions"`
	// MinScore is the mean below which a series with enough sessions gets a finding.
	MinScore float64 `json:"minScore"`
	// MaxSessions is how many of the lowest-scoring sessions a finding quotes.
	MaxSessions int `json:"maxSessions"`
	// VerifyWithin is how long a correction may stay pending before it counts as failed.
	VerifyWithin     Duration `json:"verifyWithin"`
	MinAgreement     float64  `json:"minAgreement"`
	RecalibrateEvery Duration `json:"recalibrateEvery"`
}

// Sink is one notification target.
type Sink struct {
	// Type is "discord", "webhook" (the AlertPayload body) or "event" (a
	// generic event body, see EventOf).
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
	// URLEnv names an env var holding the URL, so a secret URL (Discord's
	// carries its token) stays out of the ConfigMap.
	URLEnv string `json:"urlEnv,omitempty"`
	// BearerTokenFile is read on every send (a projected token rotates).
	BearerTokenFile string `json:"bearerTokenFile,omitempty"`
	// Digest sends the daily digest to this sink (discord only).
	Digest bool `json:"digest,omitempty"`
}

// ResolvedURL is URL, or the value of URLEnv.
func (s Sink) ResolvedURL() string {
	if s.URL != "" {
		return s.URL
	}
	if s.URLEnv != "" {
		return os.Getenv(s.URLEnv)
	}
	return ""
}

// Config is the --online-config file.
type Config struct {
	Budget   Budget    `json:"budget"`
	Sampling Sampling  `json:"sampling"`
	Profiles []Profile `json:"profiles"`
	Detector Detector  `json:"detector"`
	Findings Findings  `json:"findings"`
	Sinks    []Sink    `json:"sinks"`
	// PublicURL prefixes session links in notifications.
	PublicURL string `json:"publicUrl,omitempty"`
	// DigestHourUTC is the hour of the daily digest (default 8; -1 = off).
	DigestHourUTC *int `json:"digestHourUTC,omitempty"`
}

// Duration is a time.Duration written as a Go duration string in JSON.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"6h\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// unmeteredMetrics run on Vertex's managed service, whose cost the budget
// cannot see, so profiles may not use them.
var unmeteredMetrics = map[string]bool{
	"safety_v1":                        true,
	"multi_turn_task_success_v1":       true,
	"multi_turn_trajectory_quality_v1": true,
	"multi_turn_tool_use_quality_v1":   true,
}

// referenceMetrics need a golden eval set, which production traffic has not.
var referenceMetrics = map[string]bool{
	"final_response_match_v2": true,
	"response_match_score":    true,
	"jev_matches_reference":   true,
}

// Load reads and validates a config file, filling defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse validates a config document, filling defaults.
func Parse(b []byte) (*Config, error) {
	// The findings pass is on unless the config turns it off, and a bool cannot tell "absent" from "false" after
	// decoding.
	c := Config{Findings: Findings{Enabled: true}}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parsing online config: %w", err)
	}
	c.fillDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) fillDefaults() {
	if c.Sampling.PerKindPerHour == 0 {
		c.Sampling.PerKindPerHour = 6
	}
	if c.Sampling.BoostPerKindPerHour == 0 {
		c.Sampling.BoostPerKindPerHour = 6
	}
	if c.Sampling.AfterChangeSessions == 0 {
		c.Sampling.AfterChangeSessions = 20
	}
	if c.Sampling.Trivial.MaxUserChars == 0 {
		c.Sampling.Trivial.MaxUserChars = 40
	}
	if c.Sampling.Trivial.MaxReplyChars == 0 {
		c.Sampling.Trivial.MaxReplyChars = 100
	}
	if c.Sampling.DedupWindow == 0 {
		c.Sampling.DedupWindow = Duration(6 * time.Hour)
	}
	if c.Detector.Window == 0 {
		c.Detector.Window = Duration(6 * time.Hour)
	}
	if c.Detector.Baseline == 0 {
		c.Detector.Baseline = Duration(7 * 24 * time.Hour)
	}
	if c.Detector.MinSamples == 0 {
		c.Detector.MinSamples = 12
	}
	if c.Detector.MinDrop == 0 {
		c.Detector.MinDrop = 0.1
	}
	if c.Detector.Every == 0 {
		c.Detector.Every = Duration(10 * time.Minute)
	}
	f := &c.Findings
	if f.Every == 0 {
		f.Every = Duration(6 * time.Hour)
	}
	if f.Window == 0 {
		f.Window = Duration(24 * time.Hour)
	}
	if f.MinSessions == 0 {
		f.MinSessions = 8
	}
	if f.MinScore == 0 {
		f.MinScore = 0.7
	}
	if f.MaxSessions == 0 {
		f.MaxSessions = 8
	}
	if f.VerifyWithin == 0 {
		f.VerifyWithin = Duration(48 * time.Hour)
	}
	if f.MinAgreement == 0 {
		f.MinAgreement = 0.7
	}
	if f.RecalibrateEvery == 0 {
		f.RecalibrateEvery = Duration(24 * time.Hour)
	}
	if c.DigestHourUTC == nil {
		h := 8
		c.DigestHourUTC = &h
	}
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.MaxInvocations == 0 {
			p.MaxInvocations = 3
		}
		for j := range p.Evaluators {
			if p.Evaluators[j].NumSamples == 0 {
				p.Evaluators[j].NumSamples = 1
			}
		}
	}
}

func (c *Config) validate() error {
	if c.Budget.JudgeTokensPerDay < 0 || c.Budget.JevCallsPerDay < 0 {
		return fmt.Errorf("online config: budget caps must not be negative")
	}
	if c.Sampling.Trivial.MaxUserChars < 0 || c.Sampling.Trivial.MaxReplyChars < 0 {
		return fmt.Errorf("online config: sampling.trivial limits must not be negative")
	}
	if c.Detector.Window >= c.Detector.Baseline {
		return fmt.Errorf("online config: detector.window (%s) must be shorter than detector.baseline (%s)",
			c.Detector.Window.D(), c.Detector.Baseline.D())
	}
	if c.Detector.MinSamples < 3 {
		return fmt.Errorf("online config: detector.minSamples must be at least 3")
	}
	if err := c.Findings.validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range c.Profiles {
		if p.Kind == "" {
			return fmt.Errorf("online config: every profile needs a kind")
		}
		if seen[p.Key()] {
			return fmt.Errorf("online config: duplicate profile %q", p.Key())
		}
		seen[p.Key()] = true
		if len(p.Evaluators) == 0 {
			return fmt.Errorf("online config: profile %q has no evaluators", p.Key())
		}
		if p.UsesJev() && c.Budget.JevCallsPerDay == 0 {
			return fmt.Errorf("online config: profile %q uses jev_* evaluators but budget.jevCallsPerDay is 0", p.Key())
		}
		if p.UsesJudge() && c.Budget.JudgeTokensPerDay == 0 {
			return fmt.Errorf("online config: profile %q uses Gemini judges but budget.judgeTokensPerDay is 0", p.Key())
		}
		for _, e := range p.Evaluators {
			if unmeteredMetrics[e.Name] {
				return fmt.Errorf("online config: profile %q: %s runs on Vertex's managed service, which the budget cannot meter", p.Key(), e.Name)
			}
			if referenceMetrics[e.Name] {
				return fmt.Errorf("online config: profile %q: %s needs a golden eval set, which live traffic has not", p.Key(), e.Name)
			}
			if e.NumSamples < 1 || e.NumSamples > 5 {
				return fmt.Errorf("online config: profile %q: %s numSamples must be 1..5", p.Key(), e.Name)
			}
		}
	}
	for _, s := range c.Sinks {
		switch s.Type {
		case "discord", "webhook", "event":
		default:
			return fmt.Errorf("online config: sink type %q is not discord, webhook or event", s.Type)
		}
		if s.URL == "" && s.URLEnv == "" {
			return fmt.Errorf("online config: %s sink needs url or urlEnv", s.Type)
		}
	}
	return nil
}

func (f Findings) validate() error {
	for name, d := range map[string]Duration{"every": f.Every, "window": f.Window, "verifyWithin": f.VerifyWithin, "recalibrateEvery": f.RecalibrateEvery} {
		if d <= 0 {
			return fmt.Errorf("online config: findings.%s must be positive", name)
		}
	}
	if f.MinSessions < 1 {
		return fmt.Errorf("online config: findings.minSessions must be at least 1")
	}
	if f.MaxSessions < 1 || f.MaxSessions > 20 {
		return fmt.Errorf("online config: findings.maxSessions must be 1..20")
	}
	if f.MinScore <= 0 || f.MinScore > 1 {
		return fmt.Errorf("online config: findings.minScore must be in (0, 1]")
	}
	if f.MinAgreement <= 0 || f.MinAgreement > 1 {
		return fmt.Errorf("online config: findings.minAgreement must be in (0, 1]")
	}
	return nil
}

// Match returns the most specific profile for a session's kind, service and
// tenant: a profile naming service or tenant beats one naming only the kind.
func (c *Config) Match(kind, service, tenant string) (Profile, bool) {
	best, bestScore := Profile{}, -1
	for _, p := range c.Profiles {
		if p.Kind != kind {
			continue
		}
		if p.Service != "" && p.Service != service {
			continue
		}
		if p.Tenant != "" && p.Tenant != tenant {
			continue
		}
		score := 0
		if p.Service != "" {
			score++
		}
		if p.Tenant != "" {
			score++
		}
		if score > bestScore {
			best, bestScore = p, score
		}
	}
	return best, bestScore >= 0
}

// UsesJev reports whether a profile has a jev_* evaluator.
func (p Profile) UsesJev() bool {
	for _, e := range p.Evaluators {
		if strings.HasPrefix(e.Name, "jev_") {
			return true
		}
	}
	return false
}

// UsesJudge reports whether a profile has a Gemini judge evaluator.
func (p Profile) UsesJudge() bool {
	for _, e := range p.Evaluators {
		switch e.Name {
		case "hallucinations_v1", "rubric_based_final_response_quality_v1", "rubric_based_tool_use_quality_v1":
			return true
		}
	}
	return false
}
