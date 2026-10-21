// Package mcptools is the one MCP tool registry of ego. `ego mcp` (stdio) and `serve` (POST /mcp) register the
// same tools; they differ only in the Backend behind them: a REST client for the first, the stores themselves for
// the second.
package mcptools

import (
	"context"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Backend is what the tools need from an ego server. Every method takes the tool call's context: the in-process
// implementation reads the caller's role from it, so tenant and agent limits apply to every tool; the REST
// implementation sends the caller's credential and lets the server apply them.
type Backend interface {
	Metrics(ctx context.Context) ([]MetricInfo, error)

	// Sessions lists the sessions the caller may see, in no particular order.
	Sessions(ctx context.Context) ([]SessionListItem, error)
	// SessionTraces returns a session's traces and its span count.
	SessionTraces(ctx context.Context, sessionID string) (traces []*tracepkg.Trace, numSpans int, err error)

	Runs(ctx context.Context, limit int, statuses []string) ([]RunListItem, error)
	RunResults(ctx context.Context, runID string) (*RunResults, error)

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
}
