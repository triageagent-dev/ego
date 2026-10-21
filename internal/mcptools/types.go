package mcptools

import (
	"fmt"
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

// ParseSince reads the `since` argument of get_findings and GET /api/online/findings: an RFC 3339 time, or a
// duration such as "24h" counted back from now. Empty means no limit.
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

// SessionListItem is one entry of list_sessions.
type SessionListItem struct {
	SessionID  string `json:"session_id"`
	IsComplete bool   `json:"is_complete"`
	SpanCount  int    `json:"span_count"`
	StartedAt  string `json:"started_at"`
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
	SessionID string     `json:"session_id"`
	Tenant    string     `json:"tenant"`
	Kind      string     `json:"kind"`
	Service   string     `json:"service,omitempty"`
	Version   string     `json:"version,omitempty"`
	Model     string     `json:"model,omitempty"`
	ScoredAt  string     `json:"scored_at"`
	Metrics   []Evidence `json:"metrics"`
}

// Evidence is the compact judge evidence of one metric on one session. Which of the detail lists is filled depends
// on the metric: sentences for hallucinations_v1, rubrics for the rubric metrics, comparisons for tool_trajectory,
// decisions for jev_*. Each list puts the failing items first.
type Evidence struct {
	Metric string   `json:"metric"`
	Score  *float64 `json:"score"`
	Status string   `json:"status"`
	Error  string   `json:"error,omitempty"`
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
