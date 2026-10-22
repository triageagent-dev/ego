// Package mcptools is the one MCP tool registry of ego. `ego mcp` (stdio) and `serve` (POST /mcp) register the
// same tools; they differ only in the Backend behind them: a REST client for the first, the stores themselves for
// the second.
package mcptools

import (
	"context"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Backend is what the tools need from an ego server. Every method takes the tool call's context: the in-process
// implementation reads the caller's role from it, so tenant and agent limits apply to every tool; the REST
// implementation sends the caller's credential and lets the server apply them.
type Backend interface {
	Metrics(ctx context.Context) ([]MetricInfo, error)

	// Sessions lists the sessions the caller may see that match filter, in no particular order. The filter applies
	// after the caller's role limits.
	Sessions(ctx context.Context, filter SessionFilter) ([]SessionListItem, error)
	// Threads answers list_threads: one row per thread the caller may see, the one seen last first. Of the filter,
	// Agent, Tenant, Since (last seen) and Query apply.
	Threads(ctx context.Context, filter SessionFilter, limit int) ([]ThreadListItem, error)
	// SessionTraces returns a session's traces and its span count.
	SessionTraces(ctx context.Context, sessionID string) (traces []*tracepkg.Trace, numSpans int, err error)

	Runs(ctx context.Context, limit int, statuses []string) ([]RunListItem, error)
	RunResults(ctx context.Context, runID string) (*RunResults, error)

	// Usage answers get_usage from the token usage ledger, limited to the caller's tenants. A role limited to
	// agents is refused: the ledger cannot be attributed to an agent.
	Usage(ctx context.Context, q UsageQuery) (*Usage, error)

	// Role bindings require an admin.
	RoleBindings(ctx context.Context) ([]RoleBinding, error)
	SetRoleBinding(ctx context.Context, in RoleBindingInput) (RoleBinding, error)
	DeleteRoleBinding(ctx context.Context, id string) (map[string]bool, error)

	// Quality answers get_quality for the caller's tenants.
	Quality(ctx context.Context, kind string, hours int) (*Quality, error)
	// ExplainRegression answers explain_regression; an alert outside the caller's tenants is refused.
	ExplainRegression(ctx context.Context, alertID string) (*RegressionExplanation, error)
	// ExplainSession answers explain_session; a session outside the caller's tenants is refused.
	ExplainSession(ctx context.Context, sessionID string) (*SessionEvidence, error)

	// Findings answers get_findings: the caller's tenants' findings, last updated first. state "" or "all" is every
	// state; a zero since is no limit.
	Findings(ctx context.Context, kind, state string, since time.Time) ([]online.Finding, error)
	// RegisterCorrection answers register_correction.
	RegisterCorrection(ctx context.Context, in CorrectionInput) (*online.Correction, error)
	// Corrections answers list_corrections; state "" or "all" is every state.
	Corrections(ctx context.Context, kind, state string) ([]online.Correction, error)
	// LabelSession answers label_session: it stores a verdict on a session's online score. The label's author is
	// the caller's own identity, which the backend knows; the tool cannot name one.
	LabelSession(ctx context.Context, in LabelInput) (*online.Label, error)
	// RunFindings answers run_findings: one sweep of the findings pass now, whatever the cadence. Admin only.
	RunFindings(ctx context.Context) (*SweepReport, error)
}
