package mcptools

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
)

// CorrectionInput is the argument of register_correction (and the body of POST /api/online/corrections). Tenant is
// optional: a role limited to one tenant implies it, and a finding names it.
type CorrectionInput struct {
	FindingID string `json:"findingId,omitempty"`
	Tenant    string `json:"tenant,omitempty"`
	Kind      string `json:"kind"`
	What      string `json:"what"`
	Version   string `json:"version,omitempty"`
	By        string `json:"by,omitempty"`
}

// LabelInput is the argument of label_session (and the body of POST /api/online/labels). Score is the score the
// person says is right; omit it to only agree or disagree with the judge's.
type LabelInput struct {
	SessionID string   `json:"sessionId"`
	Metric    string   `json:"metric"`
	Verdict   string   `json:"verdict"`
	Score     *float64 `json:"score,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// ParseSince reads the `since` argument of get_findings and GET /api/online/findings: an RFC 3339 time, or a
// duration such as "24h" counted back from now. Empty means no limit.
// Clock is optionally implemented by a Backend that keeps its own time (the server's online engine, a test
// fixture with a fixed clock); a relative since window is then measured from it instead of the wall clock.
type Clock interface {
	Now() time.Time
}

func nowOf(b Backend) time.Time {
	if c, ok := b.(Clock); ok {
		return c.Now()
	}
	return time.Now()
}

func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("since must be an RFC 3339 time or a duration like 24h")
}

// The wire contract of the tools is snake_case JSON, independent of the camelCase REST DTOs the stdio client
// reads, so an agent sees the same shapes whichever transport it uses.

// MetricInfo is one entry of list_metrics.
type MetricInfo struct {
	Name            string `json:"name"`
	Category        string `json:"category"`
	RequiresEvalSet bool   `json:"requires_eval_set"`
	RequiresLLM     bool   `json:"requires_llm"`
	RequiresGCP     bool   `json:"requires_gcp"`
	RequiresRubrics bool   `json:"requires_rubrics"`
	Description     string `json:"description"`
	Working         bool   `json:"working"`
}

// SweepReport is what run_findings answers: what one sweep of the findings pass did, as the difference it made
// to the server's counters.
type SweepReport struct {
	Candidates          int `json:"candidates"`
	Written             int `json:"written"`
	SkippedBudget       int `json:"skipped_budget"`
	SkippedNoEvidence   int `json:"skipped_no_evidence"`
	SkippedUncalibrated int `json:"skipped_uncalibrated"`
}

// SessionListItem is one entry of list_sessions.
type SessionListItem struct {
	SessionID  string `json:"session_id"`
	IsComplete bool   `json:"is_complete"`
	SpanCount  int    `json:"span_count"`
	StartedAt  string `json:"started_at"`
	// Agent and Tenant join the session's distinct agent names and tenant keys with ", ".
	Agent   string `json:"agent,omitempty"`
	Tenant  string `json:"tenant,omitempty"`
	Service string `json:"service,omitempty"`
	// ErrorSpans counts spans whose status is ERROR; TotalTokens sums the input and output tokens its LLM spans
	// reported.
	ErrorSpans  int   `json:"error_spans"`
	TotalTokens int64 `json:"total_tokens"`
	// Scored is true when online evaluation scored the session, so its judge scores can be read and labelled.
	Scored bool `json:"scored"`
	// Thread is the sender's own id for the work this session is a part of; Part is its 1-based number in that
	// thread. An always-on agent's thread has many sessions (segments), each a bounded run of whole traces.
	Thread string `json:"thread,omitempty"`
	Part   int    `json:"part,omitempty"`
}

// ThreadListItem is one entry of list_threads: a thread (the sender's own session id, kept for as long as the
// sender uses it) and the totals over its segments.
type ThreadListItem struct {
	Thread    string `json:"thread"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	// Segments counts the thread's sessions; Open is true while the newest one still takes new traces.
	Segments int  `json:"segments"`
	Open     bool `json:"open"`
	// TotalTokens sums the input and output tokens of the thread's LLM calls (the usage ledger's retention applies).
	TotalTokens int64 `json:"total_tokens"`
	// LastScore is the mean online score of the newest segment that was scored; absent when none was.
	LastScore *float64 `json:"last_score,omitempty"`
	Agent     string   `json:"agent,omitempty"`
	Tenant    string   `json:"tenant,omitempty"`
}

// SessionFilter narrows list_sessions. The server applies it after the caller's role limits, so a filter can only
// ever hide sessions the caller may already see. The zero value matches everything.
type SessionFilter struct {
	// Agent is an exact agent name (gen_ai.agent.name).
	Agent string
	// Tenant is a tenant key, "<sender>/*" or "<sender>/" for every tenant of one sender.
	Tenant string
	// Since keeps sessions that started at or after it.
	Since time.Time
	// Query is a case-insensitive substring of the session id, an agent name, the service or, for a completed
	// session, its first user message.
	Query string
	// Complete, when set, keeps only completed (true) or still open (false) sessions.
	Complete *bool
	// Scored, when set, keeps only sessions online evaluation has scored (true) or has not (false).
	Scored *bool
	// Thread keeps the sessions (segments) of one thread: its id, as list_sessions and list_threads report it.
	Thread string
}

// Values is the filter as the query string of GET /api/streaming/sessions.
func (f SessionFilter) Values() url.Values {
	q := url.Values{}
	if f.Agent != "" {
		q.Set("agent", f.Agent)
	}
	if f.Tenant != "" {
		q.Set("tenant", wireTenant(f.Tenant))
	}
	if !f.Since.IsZero() {
		q.Set("since", f.Since.UTC().Format(time.RFC3339))
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Complete != nil {
		q.Set("complete", strconv.FormatBool(*f.Complete))
	}
	if f.Scored != nil {
		q.Set("scored", strconv.FormatBool(*f.Scored))
	}
	if f.Thread != "" {
		q.Set("thread", f.Thread)
	}
	return q
}

// wireTenant writes "<sender>/*" as "<sender>/": gateways refuse a "/*" in a query string, and the server reads the
// short form as the same grant (the UI does the same).
func wireTenant(t string) string {
	if strings.HasSuffix(t, "/*") {
		return strings.TrimSuffix(t, "*")
	}
	return t
}

// ParseSessionFilter reads the query string written by Values.
func ParseSessionFilter(q url.Values, now time.Time) (SessionFilter, error) {
	since, err := ParseSince(q.Get("since"), now)
	if err != nil {
		return SessionFilter{}, err
	}
	f := SessionFilter{
		Agent: strings.TrimSpace(q.Get("agent")), Tenant: strings.TrimSpace(q.Get("tenant")),
		Since: since, Query: strings.TrimSpace(q.Get("q")), Thread: strings.TrimSpace(q.Get("thread")),
	}
	if v := q.Get("complete"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return SessionFilter{}, fmt.Errorf("complete must be true or false")
		}
		f.Complete = &b
	}
	if v := q.Get("scored"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return SessionFilter{}, fmt.Errorf("scored must be true or false")
		}
		f.Scored = &b
	}
	return f, nil
}

// ToolCall is one tool call of an invocation.
type ToolCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// InvocationSummary is one user turn of summarize_session.
type InvocationSummary struct {
	User      string     `json:"user"`
	Response  string     `json:"response"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

// SessionSummary is the result of summarize_session.
type SessionSummary struct {
	SessionID      string              `json:"session_id"`
	NumSpans       int                 `json:"num_spans"`
	NumInvocations int                 `json:"num_invocations"`
	Invocations    []InvocationSummary `json:"invocations"`
	// Note says where to look when there is nothing to summarize.
	Note string `json:"note,omitempty"`
}

// RunResultCounts counts a run's results by outcome.
type RunResultCounts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
	Skipped int `json:"skipped"`
}

// RunPerMetric is one metric's outcome counts and mean within a run.
type RunPerMetric struct {
	Passed   int      `json:"passed"`
	Failed   int      `json:"failed"`
	Errored  int      `json:"errored"`
	Skipped  int      `json:"skipped"`
	AvgScore *float64 `json:"avg_score"`
}

// RunSummary is a finished run's summary.
type RunSummary struct {
	TraceCount   int                     `json:"trace_count"`
	ResultCounts RunResultCounts         `json:"result_counts"`
	PerMetric    map[string]RunPerMetric `json:"per_metric,omitempty"`
	Agents       []string                `json:"agents,omitempty"`
	Errors       []string                `json:"errors,omitempty"`
}

// RunListItem is one entry of list_runs. The run's spec is dropped on purpose: its eval set and config blobs can
// be megabytes of conversation data, and list_runs is for finding run ids and their pass/fail summary.
type RunListItem struct {
	RunID      string      `json:"run_id"`
	Status     string      `json:"status"`
	Approach   string      `json:"approach,omitempty"`
	Error      *string     `json:"error,omitempty"`
	Summary    *RunSummary `json:"summary,omitempty"`
	CreatedAt  string      `json:"created_at"`
	StartedAt  *string     `json:"started_at,omitempty"`
	FinishedAt *string     `json:"finished_at,omitempty"`
}

// RunResultRow is one (item, evaluator) result of a run.
type RunResultRow struct {
	ResultID            string         `json:"result_id"`
	EvalSetItemID       string         `json:"eval_set_item_id"`
	EvalSetItemName     string         `json:"eval_set_item_name"`
	EvaluatorName       string         `json:"evaluator_name"`
	EvaluatorType       string         `json:"evaluator_type"`
	Status              string         `json:"status"`
	Score               *float64       `json:"score"`
	PerInvocationScores []*float64     `json:"per_invocation_scores,omitempty"`
	TraceID             *string        `json:"trace_id,omitempty"`
	Details             map[string]any `json:"details,omitempty"`
	ErrorText           *string        `json:"error_text,omitempty"`
}

// RunResults is the result of get_run_results.
type RunResults struct {
	RunID      string         `json:"run_id"`
	Status     string         `json:"status"`
	Approach   string         `json:"approach,omitempty"`
	Error      *string        `json:"error,omitempty"`
	Summary    *RunSummary    `json:"summary,omitempty"`
	CreatedAt  string         `json:"created_at"`
	StartedAt  *string        `json:"started_at,omitempty"`
	FinishedAt *string        `json:"finished_at,omitempty"`
	Results    []RunResultRow `json:"results"`
}

// RoleBinding is a role grant as the role tools show it.
type RoleBinding struct {
	ID          string   `json:"id"`
	SubjectType string   `json:"subject_type"`
	Subject     string   `json:"subject"`
	Role        string   `json:"role"`
	Agents      []string `json:"agents,omitempty"`
	Tenants     []string `json:"tenants,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// RoleBindingInput is the argument of set_role_binding; an empty ID creates a binding.
type RoleBindingInput struct {
	ID          string
	SubjectType string
	Subject     string
	Role        string
	Agents      []string
	Tenants     []string
}

// Quality is the result of get_quality: how the caller's tenants are doing over the last Hours.
type Quality struct {
	Hours int    `json:"hours"`
	Kind  string `json:"kind,omitempty"`
	// OpenAlerts are the open regressions, in the shape of the alert webhook.
	OpenAlerts []online.AlertPayload `json:"open_alerts"`
	// Series is the hourly mean score per (tenant, kind, metric).
	Series []MetricSeries `json:"series"`
	// SessionsScored counts sessions scored in the window, per tenant and kind.
	SessionsScored      []ScoredCount `json:"sessions_scored"`
	SessionsScoredTotal int           `json:"sessions_scored_total"`
	// Budget is today's judge spend. It is shared by every tenant, so a tenant-limited caller does not get it.
	Budget *BudgetUse `json:"budget,omitempty"`
}

// TurnScore is the ego score of one agent from its turns, for an agent no judge scores. Score is the share of turns
// without a failure, 0 to 1; ErrorRate the share of its tool calls that failed; FailingTools the tools that failed
// most, worst first.
type TurnScore struct {
	Agent  string `json:"agent"`
	Tenant string `json:"tenant,omitempty"`
	// Session is the session this row scores (cut by session); From and To bound the turns of a session or the
	// period of a row cut by hour or day.
	Session         string   `json:"session,omitempty"`
	From            string   `json:"from,omitempty"`
	To              string   `json:"to,omitempty"`
	Hours           int      `json:"hours"`
	Turns           int      `json:"turns"`
	Score           *float64 `json:"score,omitempty"`
	ToolCalls       int      `json:"tool_calls"`
	FailedToolCalls int      `json:"failed_tool_calls"`
	ErrorRate       float64  `json:"error_rate"`
	// ModelRequests, the tokens and CachedShare (the part of the input read from cache) say what the turns cost;
	// MedianTurnMs and P95TurnMs how long a turn takes; Awake the share of the window with a turn running.
	ModelRequests int64        `json:"model_requests"`
	InputTokens   int64        `json:"input_tokens"`
	OutputTokens  int64        `json:"output_tokens"`
	CachedShare   float64      `json:"cached_share"`
	MedianTurnMs  int64        `json:"median_turn_ms"`
	P95TurnMs     int64        `json:"p95_turn_ms"`
	Awake         float64      `json:"awake"`
	FailingTools  []ToolRecord `json:"failing_tools"`
}

// EgoTools is get_ego_tools: how each tool of an agent did in the window, the ones that failed most first.
type EgoTools struct {
	Agent  string       `json:"agent"`
	Tenant string       `json:"tenant,omitempty"`
	Hours  int          `json:"hours"`
	Tools  []ToolRecord `json:"tools"`
}

// ErrorGroup is one kind of failure: the same tool with the same error text (digits aside), how often it happened and
// when, and the session of the latest one, to read it with get_session_spans.
type ErrorGroup struct {
	Tool    string `json:"tool"`
	Server  string `json:"server,omitempty"`
	Message string `json:"message"`
	Count   int    `json:"count"`
	FirstAt string `json:"first_at"`
	LastAt  string `json:"last_at"`
	Session string `json:"session"`
}

// EgoErrors is get_ego_errors: what failed for an agent in the window, the most frequent first.
type EgoErrors struct {
	Agent  string       `json:"agent"`
	Tenant string       `json:"tenant,omitempty"`
	Hours  int          `json:"hours"`
	Errors []ErrorGroup `json:"errors"`
}

// MetricSeries is one metric's hourly means over the window.
type MetricSeries struct {
	Tenant string        `json:"tenant"`
	Kind   string        `json:"kind"`
	Metric string        `json:"metric"`
	Mean   float64       `json:"mean"`
	N      int           `json:"n"`
	Points []SeriesPoint `json:"points"`
}

// SeriesPoint is the mean score of one hour.
type SeriesPoint struct {
	Hour string  `json:"hour"`
	Mean float64 `json:"mean"`
	N    int     `json:"n"`
}

// ScoredCount is how many sessions of a (tenant, kind) were scored in the window.
type ScoredCount struct {
	Tenant   string `json:"tenant"`
	Kind     string `json:"kind"`
	Sessions int    `json:"sessions"`
}

// BudgetUse is today's judge spend against the daily caps (a cap of 0 means the family is off).
type BudgetUse struct {
	Day             string `json:"day"`
	JudgeTokensUsed int64  `json:"judge_tokens_used"`
	JudgeTokensCap  int64  `json:"judge_tokens_cap"`
	JevCallsUsed    int64  `json:"jev_calls_used"`
	JevCallsCap     int64  `json:"jev_calls_cap"`
}

// RegressionExplanation is the result of explain_regression: an alert and the evidence behind its worst sessions.
type RegressionExplanation struct {
	Alert online.AlertPayload `json:"alert"`
	// Suspect is the version or model the drop is attributed to, when one stands out.
	Suspect  string            `json:"suspect,omitempty"`
	Sessions []SessionEvidence `json:"sessions"`
	// Missing lists worst sessions whose evidence is gone (pruned or never stored).
	Missing []string `json:"missing,omitempty"`
}

// SessionEvidence is everything stored about why one session scored as it did.
type SessionEvidence struct {
	SessionID string `json:"session_id"`
	Tenant    string `json:"tenant"`
	Kind      string `json:"kind"`
	Service   string `json:"service,omitempty"`
	Version   string `json:"version,omitempty"`
	Model     string `json:"model,omitempty"`
	ScoredAt  string `json:"scored_at"`
	// RunID is the hourly run the session was scored in, for a link to how that run went.
	RunID   string     `json:"run_id,omitempty"`
	Metrics []Evidence `json:"metrics"`
}

// Evidence is the compact judge evidence of one metric on one session. Which of the detail lists is filled depends
// on the metric: sentences for hallucinations_v1, rubrics for the rubric metrics, comparisons for tool_trajectory,
// decisions for jev_*. Each list puts the failing items first.
type Evidence struct {
	Metric string   `json:"metric"`
	Score  *float64 `json:"score"`
	Status string   `json:"status"`
	Error  string   `json:"error,omitempty"`
	// HumanVerdict is the newest label's verdict on the score (agree or disagree), HumanScore the score the person
	// gave with it. A person's score is the score of record wherever ego aggregates (docs/rsi.md, section 4).
	HumanVerdict string   `json:"human_verdict,omitempty"`
	HumanScore   *float64 `json:"human_score,omitempty"`
	// Rationale is the judge's reason for the first failing item, or the decision's answer for jev_*.
	Rationale string `json:"rationale,omitempty"`
	// Failing counts the failing items among Total (unsupported, contradictory or disputed sentences, "no" rubrics,
	// unmatched invocations, failed decisions).
	Failing int `json:"failing"`
	Total   int `json:"total"`
	// Truncated counts items left out of the lists to keep the evidence compact.
	Truncated   int                    `json:"truncated,omitempty"`
	Sentences   []SentenceVerdict      `json:"sentences,omitempty"`
	Rubrics     []RubricVerdict        `json:"rubrics,omitempty"`
	Comparisons []TrajectoryComparison `json:"comparisons,omitempty"`
	Decisions   []DecisionTurn         `json:"decisions,omitempty"`
}

// SentenceVerdict is the hallucination judge's verdict on one sentence of a reply.
type SentenceVerdict struct {
	InvocationID         string `json:"invocation_id,omitempty"`
	Sentence             string `json:"sentence"`
	Label                string `json:"label"`
	Rationale            string `json:"rationale,omitempty"`
	SupportingExcerpt    string `json:"supporting_excerpt,omitempty"`
	ContradictingExcerpt string `json:"contradicting_excerpt,omitempty"`
}

// RubricVerdict is a rubric judge's verdict on one rubric.
type RubricVerdict struct {
	InvocationID string `json:"invocation_id,omitempty"`
	ID           string `json:"id,omitempty"`
	Rubric       string `json:"rubric"`
	Verdict      string `json:"verdict"`
	Rationale    string `json:"rationale,omitempty"`
}

// TrajectoryComparison is the expected and actual tool calls of one invocation.
type TrajectoryComparison struct {
	InvocationID string     `json:"invocation_id,omitempty"`
	Matched      bool       `json:"matched"`
	Expected     []ToolCall `json:"expected"`
	Actual       []ToolCall `json:"actual"`
}

// DecisionTurn is a Jev decision on one invocation.
type DecisionTurn struct {
	InvocationID  string   `json:"invocation_id,omitempty"`
	Question      string   `json:"question,omitempty"`
	Expect        any      `json:"expect,omitempty"`
	UserInput     string   `json:"user_input,omitempty"`
	FinalResponse string   `json:"final_response,omitempty"`
	Score         *float64 `json:"score"`
	Answer        string   `json:"answer,omitempty"`
}

// ActivityQuery selects what get_activity summarizes: one agent (in one tenant) or, with no agent, every agent the
// caller may see that worked in turns during the window.
type ActivityQuery struct {
	Agent  string
	Tenant string
	Hours  int
	// Session narrows the turns to one session (a segment of an always-on agent's thread).
	Session string
	// By is how get_ego_score cuts the window: ScoreByAgent (one row per agent), ScoreBySession (one per session,
	// newest first), or ScoreByHour and ScoreByDay (one per period, oldest first). Limit bounds the sessions or
	// periods returned, the newest ones.
	By    string
	Limit int
}

// The cuts of get_ego_score.
const (
	ScoreByAgent   = "agent"
	ScoreBySession = "session"
	ScoreByHour    = "hour"
	ScoreByDay     = "day"
	// MaxScoreRows bounds the sessions or periods of one answer.
	MaxScoreRows = 200
)

// ToolRecord is how one tool did over a window: its calls, how many failed and how long a call took on average.
type ToolRecord struct {
	Tool    string  `json:"tool"`
	Server  string  `json:"server,omitempty"`
	Calls   int     `json:"calls"`
	Failed  int     `json:"failed,omitempty"`
	AvgMs   float64 `json:"avg_ms,omitempty"`
	Failure float64 `json:"failure_rate,omitempty"`
}

// ActivityHour counts the turns that began in one hour (one day for a window over a week) and how many of them had a
// failure.
type ActivityHour struct {
	Start  string `json:"start"`
	Turns  int    `json:"turns"`
	Failed int    `json:"failed,omitempty"`
}

// ActivitySummary is the record of one agent over a window, from its turns alone: no judge has to have scored it. A
// turn has a failure when any of its spans failed or any of its tool calls failed or was rejected.
type ActivitySummary struct {
	Agent  string `json:"agent"`
	Tenant string `json:"tenant,omitempty"`
	Hours  int    `json:"hours"`
	Turns  int    `json:"turns"`
	// TurnsWithoutFailure over Turns is Success, 0 to 1; absent when there were no turns.
	TurnsWithoutFailure int      `json:"turns_without_failure"`
	Success             *float64 `json:"success,omitempty"`
	ToolCalls           int      `json:"tool_calls"`
	FailedToolCalls     int      `json:"failed_tool_calls"`
	ModelRequests       int      `json:"model_requests"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	// CachedShare is the part of the input tokens read from the model's cache, 0 to 1.
	CachedShare  float64 `json:"cached_share"`
	MedianTurnMs int64   `json:"median_turn_ms"`
	P95TurnMs    int64   `json:"p95_turn_ms"`
	// Awake is the share of the window with a turn running, 0 to 1.
	Awake float64 `json:"awake"`
	// Tools lists the worst first (most failures, then most calls); Hourly the turns in time order.
	Tools  []ToolRecord   `json:"tools"`
	Hourly []ActivityHour `json:"hourly"`
	// Truncated is true when only the newest turns of a long window were read.
	Truncated bool `json:"truncated,omitempty"`
}
