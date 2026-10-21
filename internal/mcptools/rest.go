package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// REST is the Backend of `ego mcp`: a client of a running `serve`'s REST API, acting with the caller's token.
type REST struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewREST returns a client of the server at baseURL. token, when set, is sent as a bearer credential.
func NewREST(baseURL, token string, client *http.Client) *REST {
	return &REST{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: client}
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *string         `json:"error"`
}

func (b *REST) request(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach ego server at %s: %w (start it with: ego serve)", b.baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("server at %s returned 401 Unauthorized - pass --session-token/EGO_SESSION_TOKEN (mint one with: ego auth mint-token --name <label>)", b.baseURL)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var env envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("decoding response from %s: %w", path, err)
	}
	if env.Error != nil && *env.Error != "" {
		return fmt.Errorf("API error: %s", *env.Error)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decoding data from %s: %w", path, err)
		}
	}
	return nil
}

func (b *REST) get(ctx context.Context, path string, out any) error {
	return b.request(ctx, http.MethodGet, path, nil, out)
}

func (b *REST) post(ctx context.Context, path string, body, out any) error {
	return b.request(ctx, http.MethodPost, path, body, out)
}

func (b *REST) delete(ctx context.Context, path string, out any) error {
	return b.request(ctx, http.MethodDelete, path, nil, out)
}

func (b *REST) Metrics(ctx context.Context) ([]MetricInfo, error) {
	// The REST DTO is camelCase to match the UI; the tool contract is snake_case.
	var raw []struct {
		Name            string `json:"name"`
		Category        string `json:"category"`
		RequiresEvalSet bool   `json:"requiresEvalSet"`
		RequiresLLM     bool   `json:"requiresLLM"`
		RequiresGCP     bool   `json:"requiresGCP"`
		RequiresRubrics bool   `json:"requiresRubrics"`
		Description     string `json:"description"`
		Working         bool   `json:"working"`
	}
	if err := b.get(ctx, "/api/metrics", &raw); err != nil {
		return nil, err
	}
	out := make([]MetricInfo, len(raw))
	for i, m := range raw {
		out[i] = MetricInfo{
			Name: m.Name, Category: m.Category,
			RequiresEvalSet: m.RequiresEvalSet, RequiresLLM: m.RequiresLLM,
			RequiresGCP: m.RequiresGCP, RequiresRubrics: m.RequiresRubrics,
			Description: m.Description, Working: m.Working,
		}
	}
	return out, nil
}

func (b *REST) Sessions(ctx context.Context) ([]SessionListItem, error) {
	var raw []struct {
		SessionID  string `json:"sessionId"`
		IsComplete bool   `json:"isComplete"`
		SpanCount  int    `json:"spanCount"`
		StartedAt  string `json:"startedAt"`
	}
	if err := b.get(ctx, "/api/streaming/sessions", &raw); err != nil {
		return nil, err
	}
	out := make([]SessionListItem, len(raw))
	for i, s := range raw {
		out[i] = SessionListItem{SessionID: s.SessionID, IsComplete: s.IsComplete, SpanCount: s.SpanCount, StartedAt: s.StartedAt}
	}
	return out, nil
}

func (b *REST) SessionTraces(ctx context.Context, sessionID string) ([]*tracepkg.Trace, int, error) {
	var raw struct {
		TraceContent string `json:"traceContent"`
		NumSpans     int    `json:"numSpans"`
	}
	if err := b.post(ctx, "/api/streaming/get-trace", map[string]string{"session_id": sessionID}, &raw); err != nil {
		return nil, 0, err
	}
	traces, err := otlp.ParseJSONLSpans([]byte(raw.TraceContent))
	if err != nil {
		return nil, 0, fmt.Errorf("parsing trace content: %v", err)
	}
	return traces, raw.NumSpans, nil
}

// runDTO is the REST shape of a run (internal/api's runDTO), reduced to what the tools show.
type runDTO struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Spec   struct {
		Approach string `json:"approach,omitempty"`
	} `json:"spec"`
	Error      *string     `json:"error,omitempty"`
	Summary    *RunSummary `json:"summary,omitempty"`
	CreatedAt  string      `json:"createdAt"`
	StartedAt  *string     `json:"startedAt,omitempty"`
	FinishedAt *string     `json:"finishedAt,omitempty"`
}

func (b *REST) Runs(ctx context.Context, limit int, statuses []string) ([]RunListItem, error) {
	var raw []runDTO
	path := fmt.Sprintf("/api/runs?limit=%d", limit)
	for _, s := range statuses {
		path += "&status=" + url.QueryEscape(s)
	}
	if err := b.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]RunListItem, len(raw))
	for i, r := range raw {
		out[i] = RunListItem{
			RunID: r.RunID, Status: r.Status, Approach: r.Spec.Approach, Error: r.Error, Summary: r.Summary,
			CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		}
	}
	return out, nil
}

func (b *REST) RunResults(ctx context.Context, runID string) (*RunResults, error) {
	var run runDTO
	if err := b.get(ctx, "/api/runs/"+url.PathEscape(runID), &run); err != nil {
		return nil, err
	}
	var rows []struct {
		ResultID            string         `json:"resultId"`
		EvalSetItemID       string         `json:"evalSetItemId"`
		EvalSetItemName     string         `json:"evalSetItemName"`
		EvaluatorName       string         `json:"evaluatorName"`
		EvaluatorType       string         `json:"evaluatorType"`
		Status              string         `json:"status"`
		Score               *float64       `json:"score"`
		PerInvocationScores []*float64     `json:"perInvocationScores"`
		TraceID             *string        `json:"traceId,omitempty"`
		Details             map[string]any `json:"details,omitempty"`
		ErrorText           *string        `json:"errorText,omitempty"`
	}
	if err := b.get(ctx, "/api/runs/"+url.PathEscape(runID)+"/results", &rows); err != nil {
		return nil, err
	}
	out := &RunResults{
		RunID: run.RunID, Status: run.Status, Approach: run.Spec.Approach, Error: run.Error, Summary: run.Summary,
		CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Results: make([]RunResultRow, len(rows)),
	}
	for i, r := range rows {
		out.Results[i] = RunResultRow{
			ResultID: r.ResultID, EvalSetItemID: r.EvalSetItemID, EvalSetItemName: r.EvalSetItemName,
			EvaluatorName: r.EvaluatorName, EvaluatorType: r.EvaluatorType, Status: r.Status, Score: r.Score,
			PerInvocationScores: r.PerInvocationScores, TraceID: r.TraceID, Details: r.Details, ErrorText: r.ErrorText,
		}
	}
	return out, nil
}

// roleBindingRaw is the REST shape of a role binding (camelCase).
type roleBindingRaw struct {
	ID          string   `json:"id"`
	SubjectType string   `json:"subjectType"`
	Subject     string   `json:"subject"`
	Role        string   `json:"role"`
	Agents      []string `json:"agents,omitempty"`
	Tenants     []string `json:"tenants,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

func (r roleBindingRaw) toTool() RoleBinding {
	return RoleBinding{
		ID: r.ID, SubjectType: r.SubjectType, Subject: r.Subject, Role: r.Role,
		Agents: r.Agents, Tenants: r.Tenants, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (b *REST) RoleBindings(ctx context.Context) ([]RoleBinding, error) {
	var raw []roleBindingRaw
	if err := b.get(ctx, "/api/admin/roles", &raw); err != nil {
		return nil, err
	}
	out := make([]RoleBinding, len(raw))
	for i, r := range raw {
		out[i] = r.toTool()
	}
	return out, nil
}

func (b *REST) SetRoleBinding(ctx context.Context, in RoleBindingInput) (RoleBinding, error) {
	body := map[string]any{"subjectType": in.SubjectType, "subject": in.Subject, "role": in.Role}
	if len(in.Agents) > 0 {
		body["agents"] = in.Agents
	}
	if len(in.Tenants) > 0 {
		body["tenants"] = in.Tenants
	}
	if in.ID != "" {
		body["id"] = in.ID
	}
	var raw roleBindingRaw
	if err := b.post(ctx, "/api/admin/roles", body, &raw); err != nil {
		return RoleBinding{}, err
	}
	return raw.toTool(), nil
}

func (b *REST) DeleteRoleBinding(ctx context.Context, id string) (map[string]bool, error) {
	var out map[string]bool
	if err := b.delete(ctx, "/api/admin/roles/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// The quality and explain endpoints answer in the tools' own shape, so they decode straight into it.

func (b *REST) Quality(ctx context.Context, kind string, hours int) (*Quality, error) {
	q := url.Values{"hours": {strconv.Itoa(hours)}}
	if kind != "" {
		q.Set("kind", kind)
	}
	var out Quality
	if err := b.get(ctx, "/api/online/quality?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (b *REST) ExplainRegression(ctx context.Context, alertID string) (*RegressionExplanation, error) {
	var out RegressionExplanation
	if err := b.get(ctx, "/api/online/alerts/"+url.PathEscape(alertID)+"/explain", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (b *REST) ExplainSession(ctx context.Context, sessionID string) (*SessionEvidence, error) {
	var out SessionEvidence
	if err := b.get(ctx, "/api/online/sessions/"+url.PathEscape(sessionID)+"/explain", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (b *REST) Findings(ctx context.Context, kind, state string, since time.Time) ([]online.Finding, error) {
	q := url.Values{}
	if kind != "" {
		q.Set("kind", kind)
	}
	if state != "" {
		q.Set("state", state)
	}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	var out []online.Finding
	if err := b.get(ctx, "/api/online/findings?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *REST) RegisterCorrection(ctx context.Context, in CorrectionInput) (*online.Correction, error) {
	var out online.Correction
	if err := b.post(ctx, "/api/online/corrections", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (b *REST) Corrections(ctx context.Context, kind, state string) ([]online.Correction, error) {
	q := url.Values{}
	if kind != "" {
		q.Set("kind", kind)
	}
	if state != "" {
		q.Set("state", state)
	}
	var out []online.Correction
	if err := b.get(ctx, "/api/online/corrections?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *REST) LabelSession(ctx context.Context, in LabelInput) (*online.Label, error) {
	var out online.Label
	if err := b.post(ctx, "/api/online/labels", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
