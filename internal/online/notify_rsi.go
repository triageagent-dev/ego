package online

import (
	"fmt"
	"strings"
	"time"
)

// Payloads of the findings and corrections half of the loop (docs/rsi.md, sections 2 and 3). They go through the
// same sinks as alerts: Discord text, webhook JSON and the generic event body.

// Payload types, versioned like WebhookType.
const (
	FindingWebhookType    = "ego.finding.v1"
	CorrectionWebhookType = "ego.correction.v1"
)

// Generic event types.
const (
	EventTypeFindingOpened      = "EvalFindingOpened"
	EventTypeFindingResolved    = "EvalFindingResolved"
	EventTypeCorrectionVerified = "EvalCorrectionVerified"
	EventTypeCorrectionFailed   = "EvalCorrectionFailed"
)

// FindingPayload is the webhook body for one finding event: event is open, updated, resolved or stale.
type FindingPayload struct {
	Type                string   `json:"type"`
	Event               string   `json:"event"`
	FindingID           string   `json:"findingId"`
	Tenant              string   `json:"tenant"`
	Kind                string   `json:"kind"`
	Metric              string   `json:"metric"`
	FailureMode         string   `json:"failureMode"`
	Summary             string   `json:"summary"`
	SuggestedCorrection string   `json:"suggestedCorrection"`
	Confidence          string   `json:"confidence"`
	Sessions            []string `json:"sessions"`
	// Scored, Mean and Below describe the series over the finding's window: sessions scored, their mean, how
	// many were under the threshold.
	Scored int     `json:"scored"`
	Mean   float64 `json:"mean"`
	Below  int     `json:"below"`
	// Confirmed counts the evidence sessions a person scored or confirmed.
	Confirmed int    `json:"confirmed"`
	Link      string `json:"link,omitempty"`
}

// FindingPayloadOf builds the webhook body for a finding event.
func FindingPayloadOf(event string, f *Finding, cfg *Config) FindingPayload {
	sessions := f.Sessions
	if sessions == nil {
		sessions = []string{}
	}
	return FindingPayload{
		Type: FindingWebhookType, Event: event, FindingID: f.ID, Tenant: f.Tenant, Kind: f.Kind, Metric: f.Metric,
		FailureMode: f.Diagnosis.FailureMode, Summary: f.Diagnosis.Summary,
		SuggestedCorrection: f.Diagnosis.SuggestedCorrection, Confidence: f.Diagnosis.Confidence,
		Sessions: sessions, Scored: f.Window.N, Mean: f.Window.Mean, Below: f.Window.Below, Confirmed: f.Confirmed,
		Link: cfg.PublicURL,
	}
}

// FindingEventOf maps a finding event to a generic event: open and updated warn, resolved and stale are info, on
// one source per series so a receiver opens and closes one record.
func FindingEventOf(p FindingPayload) GenericEvent {
	e := GenericEvent{Source: p.Kind + "/" + p.Metric + "/finding", Tenant: p.Tenant, Raw: p}
	switch p.Event {
	case EventResolved, EventStale:
		e.Type, e.Severity = EventTypeFindingResolved, "info"
		e.Message = fmt.Sprintf("%s %s finding %s: %s", p.Kind, p.Metric, p.Event, p.FailureMode)
	default:
		e.Type, e.Severity = EventTypeFindingOpened, "warning"
		e.Message = fmt.Sprintf("%s %s finding: %s", p.Kind, p.Metric, p.FailureMode)
		if p.SuggestedCorrection != "" {
			e.Message += "; suggested: " + p.SuggestedCorrection
		}
	}
	return e
}

// FindingText renders a finding event as one short Discord message: a header with the kind, metric and tenant,
// the failure mode, the suggestion cut to a few lines, one line of numbers and the link. The evidence session ids
// are not listed: they are long, and the Quality view behind the link has them. Every value may carry text a
// model copied from a trace, so all of it goes through chatText.
func FindingText(p FindingPayload) string {
	title := "Finding"
	switch p.Event {
	case EventResolved:
		title = "Finding resolved"
	case EventStale:
		title = "Finding stale"
	case EventUpdated:
		title = "Finding updated"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** · %s · `%s`", title, chatText(p.Kind), chatText(p.Metric))
	if p.Tenant != "" {
		fmt.Fprintf(&b, " · %s", chatText(p.Tenant))
	}
	fmt.Fprintf(&b, "\n%s", chatTextN(p.FailureMode, 300))
	if p.Event != EventResolved && p.Event != EventStale && p.SuggestedCorrection != "" {
		fmt.Fprintf(&b, "\n**Suggested:** %s", chatTextN(p.SuggestedCorrection, 280))
	}
	if p.Scored > 0 {
		fmt.Fprintf(&b, "\n%d scored, mean %.2f, %d below threshold", p.Scored, p.Mean, p.Below)
		if n := len(p.Sessions); n > 0 {
			fmt.Fprintf(&b, " · %d evidence sessions", n)
			if p.Confirmed > 0 {
				fmt.Fprintf(&b, ", %d checked by a person", p.Confirmed)
			}
		}
		if p.Confidence != "" {
			fmt.Fprintf(&b, " · confidence %s", chatText(p.Confidence))
		}
	}
	if p.Link != "" {
		fmt.Fprintf(&b, "\n%s", p.Link)
	}
	return b.String()
}

// CorrectionPayload is the webhook body for a correction's verdict: event is verified or failed.
type CorrectionPayload struct {
	Type         string     `json:"type"`
	Event        string     `json:"event"`
	CorrectionID string     `json:"correctionId"`
	FindingID    string     `json:"findingId,omitempty"`
	Tenant       string     `json:"tenant"`
	Kind         string     `json:"kind"`
	Metric       string     `json:"metric,omitempty"`
	What         string     `json:"what"`
	Version      string     `json:"version,omitempty"`
	By           string     `json:"by,omitempty"`
	Before       VWindow    `json:"before"`
	After        VWindow    `json:"after"`
	Reason       string     `json:"reason,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	VerifiedAt   *time.Time `json:"verifiedAt,omitempty"`
	Link         string     `json:"link,omitempty"`
}

// CorrectionPayloadOf builds the webhook body for a correction's verdict.
func CorrectionPayloadOf(event string, c *Correction, cfg *Config) CorrectionPayload {
	return CorrectionPayload{
		Type: CorrectionWebhookType, Event: event, CorrectionID: c.ID, FindingID: c.FindingID, Tenant: c.Tenant,
		Kind: c.Kind, Metric: c.Metric, What: c.What, Version: c.Version, By: c.By,
		Before: c.Verification.Before, After: c.Verification.After, Reason: c.Verification.Reason,
		CreatedAt: c.CreatedAt, VerifiedAt: c.VerifiedAt, Link: cfg.PublicURL,
	}
}

// CorrectionEventOf maps a verdict to a generic event: verified is info, failed warns.
func CorrectionEventOf(p CorrectionPayload) GenericEvent {
	source := p.Kind + "/correction"
	if p.Metric != "" {
		source = p.Kind + "/" + p.Metric + "/correction"
	}
	e := GenericEvent{Source: source, Tenant: p.Tenant, Raw: p}
	if p.Event == CorrectionVerified {
		e.Type, e.Severity = EventTypeCorrectionVerified, "info"
		e.Message = fmt.Sprintf("%s correction verified: %.2f → %.2f (n=%d → n=%d)", p.Kind, p.Before.Mean, p.After.Mean, p.Before.N, p.After.N)
		return e
	}
	e.Type, e.Severity = EventTypeCorrectionFailed, "warning"
	e.Message = fmt.Sprintf("%s correction failed: %.2f → %.2f (n=%d → n=%d)", p.Kind, p.Before.Mean, p.After.Mean, p.Before.N, p.After.N)
	if p.Reason != "" {
		e.Message += "; " + p.Reason
	}
	return e
}

// CorrectionText renders a verdict as one Discord message.
func CorrectionText(p CorrectionPayload) string {
	scope := chatText(p.Kind)
	if p.Tenant != "" {
		scope = chatText(p.Tenant) + " · " + scope
	}
	head := "**Correction failed**"
	if p.Event == CorrectionVerified {
		head = "**Correction verified**"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s", head, scope)
	if p.Metric != "" {
		fmt.Fprintf(&b, " · `%s`", chatText(p.Metric))
	}
	fmt.Fprintf(&b, "\n%.2f → %.2f (before n=%d, after n=%d)", p.Before.Mean, p.After.Mean, p.Before.N, p.After.N)
	if p.Reason != "" {
		fmt.Fprintf(&b, ", %s", chatText(p.Reason))
	}
	fmt.Fprintf(&b, "\nChange: %s", chatTextN(p.What, 300))
	if p.Version != "" {
		fmt.Fprintf(&b, " (version %s)", chatText(p.Version))
	}
	if p.Link != "" {
		fmt.Fprintf(&b, "\n%s", p.Link)
	}
	return b.String()
}
