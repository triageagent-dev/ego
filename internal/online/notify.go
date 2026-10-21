package online

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// WebhookType versions the webhook payload.
const WebhookType = "ego.quality.v1"

// AlertPayload is the generic webhook body for one alert event.
type AlertPayload struct {
	Type       string     `json:"type"`
	Event      string     `json:"event"`
	AlertID    string     `json:"alertId"`
	Tenant     string     `json:"tenant"`
	Kind       string     `json:"kind"`
	Metric     string     `json:"metric"`
	Baseline   Window     `json:"baseline"`
	Current    Window     `json:"current"`
	Drop       float64    `json:"drop"`
	Z          float64    `json:"z"`
	Suspect    string     `json:"suspect,omitempty"`
	Sessions   []string   `json:"sessions,omitempty"`
	OpenedAt   time.Time  `json:"openedAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	Link       string     `json:"link,omitempty"`
}

// Window is one side of a test: its span, mean and sample count.
type Window struct {
	Span string  `json:"span"`
	Mean float64 `json:"mean"`
	N    int     `json:"n"`
}

// PayloadOf builds the webhook body for an alert event.
func PayloadOf(event string, a *Alert, cfg *Config) AlertPayload {
	return AlertPayload{
		Type:       WebhookType,
		Event:      event,
		AlertID:    a.ID,
		Tenant:     a.Key.Tenant,
		Kind:       a.Key.Kind,
		Metric:     a.Key.Metric,
		Baseline:   Window{Span: cfg.Detector.Baseline.D().String(), Mean: a.Last.BaseMean, N: a.Last.NBase},
		Current:    Window{Span: cfg.Detector.Window.D().String(), Mean: a.Last.CurMean, N: a.Last.NCur},
		Drop:       a.Last.Drop,
		Z:          a.Last.Z,
		Suspect:    a.Last.Suspect,
		Sessions:   a.Last.Worst,
		OpenedAt:   a.OpenedAt,
		UpdatedAt:  a.UpdatedAt,
		ResolvedAt: a.ResolvedAt,
		Link:       cfg.PublicURL,
	}
}

// Event types of the generic event body. An open or worse alert degrades,
// a resolved one recovers, so a receiver can open and close one record per
// source.
const (
	EventTypeDegraded  = "EvalQualityDegraded"
	EventTypeRecovered = "EvalQualityRecovered"
)

// GenericEvent is the "event" sink body: the common event-ingest shape
// (type, message, severity, source, tenant, raw).
type GenericEvent struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Tenant   string `json:"tenant,omitempty"`
	// Raw is the webhook body of the event: an AlertPayload, FindingPayload or CorrectionPayload.
	Raw any `json:"raw"`
}

// EventOf maps an alert event to a generic event. The source is
// "<kind>/<metric>", so each series has its own record at the receiver.
func EventOf(p AlertPayload) GenericEvent {
	e := GenericEvent{Source: p.Kind + "/" + p.Metric, Tenant: p.Tenant, Raw: p}
	if p.Event == EventResolved {
		e.Type, e.Severity = EventTypeRecovered, "info"
		e.Message = fmt.Sprintf("%s quality %s recovered: %.2f vs %.2f baseline",
			p.Kind, p.Metric, p.Current.Mean, p.Baseline.Mean)
		return e
	}
	e.Type, e.Severity = EventTypeDegraded, "warning"
	e.Message = fmt.Sprintf("%s quality %s dropped %.2f → %.2f (last %s, n=%d; baseline %s, n=%d)",
		p.Kind, p.Metric, p.Baseline.Mean, p.Current.Mean, p.Current.Span, p.Current.N, p.Baseline.Span, p.Baseline.N)
	if p.Suspect != "" {
		e.Message += "; since " + p.Suspect
	}
	return e
}

// chatText makes a value that came from span attributes safe to put in a chat message: control characters and line
// breaks become spaces, characters that start formatting, links or mentions are dropped, and it is cut to 80 runes.
// The same holds for what a model wrote about those values.
func chatText(s string) string { return chatTextN(s, 80) }

// chatTextN is chatText cut to limit runes, for longer values such as a suggested correction.
func chatTextN(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			r = ' '
		case strings.ContainsRune("`*~|>[]<@\\", r):
			continue
		}
		if n++; n > limit {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// DiscordText renders an alert event as one Discord message.
func DiscordText(p AlertPayload) string {
	kind, metric, tenant, suspect := chatText(p.Kind), chatText(p.Metric), chatText(p.Tenant), chatText(p.Suspect)
	scope := kind
	if tenant != "" {
		scope = tenant + " · " + kind
	}
	var b strings.Builder
	switch p.Event {
	case EventOpen:
		fmt.Fprintf(&b, "**Quality drop** · %s · `%s`\n", scope, metric)
	case EventUpdated:
		fmt.Fprintf(&b, "**Quality drop, worse** · %s · `%s`\n", scope, metric)
	case EventResolved:
		fmt.Fprintf(&b, "**Quality recovered** · %s · `%s`\n", scope, metric)
	}
	fmt.Fprintf(&b, "%.2f → %.2f (last %s, n=%d; baseline %s, n=%d)",
		p.Baseline.Mean, p.Current.Mean, p.Current.Span, p.Current.N, p.Baseline.Span, p.Baseline.N)
	if p.Event != EventResolved {
		if suspect != "" {
			fmt.Fprintf(&b, "\nSince %s", suspect)
		}
		if len(p.Sessions) > 0 {
			ids := make([]string, len(p.Sessions))
			for i, s := range p.Sessions {
				ids[i] = chatText(s)
			}
			fmt.Fprintf(&b, "\nLowest: `%s`", strings.Join(ids, "`, `"))
		}
	}
	if p.Link != "" {
		fmt.Fprintf(&b, "\n%s", p.Link)
	}
	return b.String()
}

// DigestRow is one series in the daily digest.
type DigestRow struct {
	Key   SeriesKey
	Mean  float64
	Prev  float64
	N     int
	PrevN int
}

// DigestText renders the daily digest.
func DigestText(rows []DigestRow, budget BudgetStatus, open int, link string) string {
	var b strings.Builder
	b.WriteString("**Agent quality, last 24h**\n")
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Key.Tenant != rows[j].Key.Tenant {
			return rows[i].Key.Tenant < rows[j].Key.Tenant
		}
		if rows[i].Key.Kind != rows[j].Key.Kind {
			return rows[i].Key.Kind < rows[j].Key.Kind
		}
		return rows[i].Key.Metric < rows[j].Key.Metric
	})
	if len(rows) == 0 {
		b.WriteString("No sessions scored.\n")
	}
	for _, r := range rows {
		scope := chatText(r.Key.Kind)
		if r.Key.Tenant != "" {
			scope = chatText(r.Key.Tenant) + " · " + scope
		}
		delta := ""
		if r.PrevN > 0 {
			delta = fmt.Sprintf(" (%+.2f)", r.Mean-r.Prev)
		}
		fmt.Fprintf(&b, "%s · `%s` %.2f%s, n=%d\n", scope, chatText(r.Key.Metric), r.Mean, delta, r.N)
	}
	fmt.Fprintf(&b, "Judge spend today: %s tokens", humanInt(budget.Spent.JudgeTokens))
	if budget.Caps.JudgeTokensPerDay > 0 {
		fmt.Fprintf(&b, " of %s", humanInt(budget.Caps.JudgeTokensPerDay))
	}
	fmt.Fprintf(&b, ", %d Jev calls", budget.Spent.JevCalls)
	if budget.Caps.JevCallsPerDay > 0 {
		fmt.Fprintf(&b, " of %d", budget.Caps.JevCallsPerDay)
	}
	fmt.Fprintf(&b, "\nOpen alerts: %d", open)
	if link != "" {
		fmt.Fprintf(&b, "\n%s", link)
	}
	return b.String()
}

func humanInt(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return fmt.Sprint(n)
	}
}

// Notifier delivers alert events and digests to the configured sinks.
type Notifier struct {
	Sinks  []Sink
	Client *http.Client
}

// discordLimit is Discord's message length cap.
const discordLimit = 2000

// Alert sends one alert event to every sink. Errors are joined; one failed
// sink does not stop the others.
func (n *Notifier) Alert(ctx context.Context, p AlertPayload) error {
	return n.fanOut(ctx, DiscordText(p), p, EventOf(p))
}

// Finding sends one finding event to every sink.
func (n *Notifier) Finding(ctx context.Context, p FindingPayload) error {
	return n.fanOut(ctx, FindingText(p), p, FindingEventOf(p))
}

// Correction sends a correction's verdict to every sink.
func (n *Notifier) Correction(ctx context.Context, p CorrectionPayload) error {
	return n.fanOut(ctx, CorrectionText(p), p, CorrectionEventOf(p))
}

// fanOut sends the chat text to discord sinks, the webhook body to webhook sinks and the generic event to event
// sinks.
func (n *Notifier) fanOut(ctx context.Context, text string, webhook any, event GenericEvent) error {
	var errs []string
	for _, s := range n.Sinks {
		var err error
		switch s.Type {
		case "discord":
			err = n.discord(ctx, s, text)
		case "webhook":
			err = n.post(ctx, s, webhook)
		case "event":
			err = n.post(ctx, s, event)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", s.Type, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Digest sends text to the discord sinks marked digest.
func (n *Notifier) Digest(ctx context.Context, text string) error {
	var errs []string
	for _, s := range n.Sinks {
		if s.Type == "discord" && s.Digest {
			if err := n.discord(ctx, s, text); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// HasDigestSink reports whether any sink takes the digest.
func (n *Notifier) HasDigestSink() bool {
	for _, s := range n.Sinks {
		if s.Type == "discord" && s.Digest {
			return true
		}
	}
	return false
}

func (n *Notifier) discord(ctx context.Context, s Sink, text string) error {
	if len(text) > discordLimit {
		text = text[:discordLimit-1] + "…"
	}
	return n.post(ctx, s, map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}})
}

func (n *Notifier) post(ctx context.Context, s Sink, body any) error {
	url := s.ResolvedURL()
	if url == "" {
		return fmt.Errorf("no url (urlEnv %q is empty)", s.URLEnv)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.BearerTokenFile != "" {
		tok, err := os.ReadFile(s.BearerTokenFile)
		if err != nil {
			return fmt.Errorf("reading bearer token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	}
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
