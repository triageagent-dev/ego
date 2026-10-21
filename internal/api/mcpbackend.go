package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// mcpBackend is the MCP tools' backend inside `serve`: it reads the stores directly, with the role the auth layer
// put in the call's context, so tenant and agent limits apply to every tool exactly as they do on the REST
// endpoints. A call without a role (no-login mode) is an admin, like every other route.
type mcpBackend struct {
	sessions *SessionStore
	archive  *SQLiteStore
	online   *OnlineEngine
	roles    *RoleStore
}

// newMCPBackend returns the in-process mcptools.Backend. archive, online and roles may be nil, as they are when
// the server runs without --session-db, --online-config or a session secret; the tools that need them then say so.
func newMCPBackend(sessions *SessionStore, archive *SQLiteStore, onlineEngine *OnlineEngine, roles *RoleStore) *mcpBackend {
	return &mcpBackend{sessions: sessions, archive: archive, online: onlineEngine, roles: roles}
}

var _ mcptools.Backend = (*mcpBackend)(nil)

var errRunsUnavailable = errors.New("Run history storage is not configured (start with --session-db to enable /api/runs)")

func (b *mcpBackend) Metrics(ctx context.Context) ([]mcptools.MetricInfo, error) {
	out := make([]mcptools.MetricInfo, len(allMetrics))
	for i, m := range allMetrics {
		out[i] = mcptools.MetricInfo{
			Name: m.Name, Category: m.Category,
			RequiresEvalSet: m.RequiresEvalSet, RequiresLLM: m.RequiresLLM,
			RequiresGCP: m.RequiresGCP, RequiresRubrics: m.RequiresRubrics,
			Description: m.Description, Working: workingMetrics[m.Name],
		}
	}
	return out, nil
}

func (b *mcpBackend) Sessions(ctx context.Context, filter mcptools.SessionFilter) ([]mcptools.SessionListItem, error) {
	out := []mcptools.SessionListItem{}
	for _, s := range b.sessions.Find(filter, allowedBy(roleInfoOrOpen(ctx))) {
		out = append(out, mcptools.SessionListItem{
			SessionID: s.SessionID, IsComplete: s.IsComplete, SpanCount: s.SpanCount, StartedAt: s.StartedAt,
			Agent: strings.Join(s.Agents, ", "), Tenant: strings.Join(s.Tenants, ", "), Service: s.Service,
			ErrorSpans: s.Errors, TotalTokens: s.TotalTokens,
		})
	}
	return out, nil
}

// Usage reads the same two ledger queries as GET /api/usage and /api/usage/calls?group=session, with the same role
// limits, and builds the answer with the code the REST backend uses.
func (b *mcpBackend) Usage(ctx context.Context, q mcptools.UsageQuery) (*mcptools.Usage, error) {
	info := roleInfoOrOpen(ctx)
	if info.Scoped() {
		return nil, errors.New("this view is not available to an agent-scoped role")
	}
	if b.archive == nil {
		return nil, errRunsUnavailable
	}
	to := time.Now()
	from := to.Add(-time.Duration(q.Hours) * time.Hour)
	tenants := narrowTenants(info.Tenants, q.Tenant)
	buckets, err := b.archive.UsageSeries(from, to, int64(24*time.Hour/time.Millisecond), tenants)
	if err != nil {
		return nil, fmt.Errorf("failed to read usage: %w", err)
	}
	rows, err := b.archive.TopUsageSessions(from, to, mcptools.UsageSessionRows, tenants)
	if err != nil {
		return nil, fmt.Errorf("failed to read usage sessions: %w", err)
	}
	cells := make([]mcptools.UsageCell, len(buckets))
	for i, c := range buckets {
		cells[i] = mcptools.UsageCell(c)
	}
	sessions := make([]mcptools.UsageSessionRow, len(rows))
	for i, r := range rows {
		sessions[i] = mcptools.UsageSessionRow(r)
	}
	return mcptools.BuildUsage(q, cells, sessions), nil
}

func (b *mcpBackend) SessionTraces(ctx context.Context, sessionID string) ([]*tracepkg.Trace, int, error) {
	tr, ok := b.sessions.Trace(sessionID)
	if !ok || !sessionVisible(b.sessions, roleInfoOrOpen(ctx), sessionID) {
		return nil, 0, errors.New("session not found")
	}
	return []*tracepkg.Trace{tr}, len(tr.AllSpans), nil
}

func (b *mcpBackend) Runs(ctx context.Context, limit int, statuses []string) ([]mcptools.RunListItem, error) {
	if b.archive == nil {
		return nil, errRunsUnavailable
	}
	runs, err := b.archive.ListRunsFor(roleInfoOrOpen(ctx), statuses, clampLimit(limit, 100), "")
	if err != nil {
		return nil, fmt.Errorf("failed to list runs: %w", err)
	}
	out := make([]mcptools.RunListItem, len(runs))
	for i, r := range runs {
		out[i] = mcptools.RunListItem{
			RunID: r.RunID, Status: r.Status, Approach: r.Spec.Approach, Error: r.Error, Summary: toolRunSummary(r.Summary),
			CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		}
	}
	return out, nil
}

func (b *mcpBackend) RunResults(ctx context.Context, runID string) (*mcptools.RunResults, error) {
	if b.archive == nil {
		return nil, errRunsUnavailable
	}
	run, ok, err := b.archive.GetRunFor(roleInfoOrOpen(ctx), runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch run: %w", err)
	}
	if !ok {
		return nil, errors.New("run not found")
	}
	rows, err := b.archive.GetRunResults(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch run results: %w", err)
	}
	out := &mcptools.RunResults{
		RunID: run.RunID, Status: run.Status, Approach: run.Spec.Approach, Error: run.Error, Summary: toolRunSummary(run.Summary),
		CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Results: make([]mcptools.RunResultRow, len(rows)),
	}
	for i, r := range rows {
		out.Results[i] = mcptools.RunResultRow{
			ResultID: r.ResultID, EvalSetItemID: r.EvalSetItemID, EvalSetItemName: r.EvalSetItemName,
			EvaluatorName: r.EvaluatorName, EvaluatorType: r.EvaluatorType, Status: r.Status, Score: r.Score,
			PerInvocationScores: r.PerInvocationScores, TraceID: r.TraceID, Details: r.Details, ErrorText: r.ErrorText,
		}
	}
	return out, nil
}

func toolRunSummary(s *runSummaryDTO) *mcptools.RunSummary {
	if s == nil {
		return nil
	}
	out := &mcptools.RunSummary{
		TraceCount:   s.TraceCount,
		ResultCounts: mcptools.RunResultCounts(s.ResultCounts),
		Agents:       s.Agents,
		Errors:       s.Errors,
	}
	if s.PerMetric != nil {
		out.PerMetric = make(map[string]mcptools.RunPerMetric, len(s.PerMetric))
		for name, pm := range s.PerMetric {
			out.PerMetric[name] = mcptools.RunPerMetric{
				Passed: pm.Passed, Failed: pm.Failed, Errored: pm.Errored, Skipped: pm.Skipped, AvgScore: pm.AvgScore,
			}
		}
	}
	return out
}

// adminRoles is the role store, for a caller who may manage it.
func (b *mcpBackend) adminRoles(ctx context.Context) (*RoleStore, error) {
	if !roleInfoOrOpen(ctx).IsAdmin {
		return nil, errors.New("admin role required")
	}
	if b.roles == nil {
		return nil, errors.New("role management requires --session-secret (and GitHub OAuth) to be enabled")
	}
	return b.roles, nil
}

func toolRoleBinding(r RoleBinding) mcptools.RoleBinding {
	return mcptools.RoleBinding{
		ID: r.ID, SubjectType: r.SubjectType, Subject: r.Subject, Role: string(r.Role),
		Agents: r.Agents, Tenants: r.Tenants, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (b *mcpBackend) RoleBindings(ctx context.Context) ([]mcptools.RoleBinding, error) {
	roles, err := b.adminRoles(ctx)
	if err != nil {
		return nil, err
	}
	list := roles.List()
	out := make([]mcptools.RoleBinding, len(list))
	for i, r := range list {
		out[i] = toolRoleBinding(r)
	}
	return out, nil
}

func (b *mcpBackend) SetRoleBinding(ctx context.Context, in mcptools.RoleBindingInput) (mcptools.RoleBinding, error) {
	roles, err := b.adminRoles(ctx)
	if err != nil {
		return mcptools.RoleBinding{}, err
	}
	role, err := ParseRole(in.Role)
	if err != nil {
		return mcptools.RoleBinding{}, err
	}
	binding, err := roles.Upsert(RoleBinding{
		ID: in.ID, SubjectType: in.SubjectType, Subject: in.Subject, Role: role, Agents: in.Agents, Tenants: in.Tenants,
	})
	if err != nil {
		return mcptools.RoleBinding{}, err
	}
	return toolRoleBinding(binding), nil
}

func (b *mcpBackend) DeleteRoleBinding(ctx context.Context, id string) (map[string]bool, error) {
	roles, err := b.adminRoles(ctx)
	if err != nil {
		return nil, err
	}
	ok, err := roles.Delete(id)
	if err != nil {
		return nil, fmt.Errorf("failed to delete role binding: %w", err)
	}
	if !ok {
		return nil, errors.New("role binding not found")
	}
	return map[string]bool{"deleted": true}, nil
}

func (b *mcpBackend) Quality(ctx context.Context, kind string, hours int) (*mcptools.Quality, error) {
	return b.online.Quality(roleInfoOrOpen(ctx), kind, hours)
}

func (b *mcpBackend) ExplainRegression(ctx context.Context, alertID string) (*mcptools.RegressionExplanation, error) {
	return b.online.ExplainRegression(roleInfoOrOpen(ctx), alertID)
}

func (b *mcpBackend) ExplainSession(ctx context.Context, sessionID string) (*mcptools.SessionEvidence, error) {
	return b.online.ExplainSession(roleInfoOrOpen(ctx), sessionID)
}

func (b *mcpBackend) Findings(ctx context.Context, kind, state string, since time.Time) ([]online.Finding, error) {
	return b.online.Findings(roleInfoOrOpen(ctx), kind, state, since)
}

func (b *mcpBackend) RegisterCorrection(ctx context.Context, in mcptools.CorrectionInput) (*online.Correction, error) {
	return b.online.RegisterCorrection(roleInfoOrOpen(ctx), in)
}

func (b *mcpBackend) RunFindings(ctx context.Context) (*mcptools.SweepReport, error) {
	return b.online.SweepNow(ctx, roleInfoOrOpen(ctx))
}

func (b *mcpBackend) Corrections(ctx context.Context, kind, state string) ([]online.Correction, error) {
	return b.online.Corrections(roleInfoOrOpen(ctx), kind, state)
}

func (b *mcpBackend) LabelSession(ctx context.Context, in mcptools.LabelInput) (*online.Label, error) {
	return b.online.LabelSession(roleInfoOrOpen(ctx), callerName(ctx), in)
}
