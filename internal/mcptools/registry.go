package mcptools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/online"
	"github.com/triageagent-dev/ego/skills"
)

// ServerVersion is reported to MCP clients during initialize; it is not otherwise meaningful.
const ServerVersion = "0.1.0"

// defaultFindingState is the state get_findings lists when none is asked for: the work that is waiting.
const defaultFindingState = "open"

// Defaults and bounds of get_quality's window.
const (
	defaultQualityHours = 24
	maxQualityHours     = 720
)

// Options choose which tools a transport registers.
type Options struct {
	// Admin registers the role-binding tools. The backend still requires an admin to use them.
	Admin bool
	// EvaluateTraces is the handler of evaluate_traces, which reads trace files from the local disk and needs no
	// server. Only the stdio command has a local disk to offer: leave it nil on the server, where the tool is not
	// registered at all.
	EvaluateTraces server.ToolHandlerFunc
}

// RSILoopPrompt is the name of the MCP prompt that carries the skill.
const RSILoopPrompt = "rsi_loop"

// NewServer builds an MCP server with every tool and prompt of the registry.
func NewServer(b Backend, o Options) *server.MCPServer {
	s := server.NewMCPServer("ego", ServerVersion, server.WithToolCapabilities(false), server.WithPromptCapabilities(false))
	s.AddTools(Tools(b, o)...)
	s.AddPrompt(Prompt(), rsiLoopHandler)
	return s
}

// Prompt is the rsi_loop prompt: the text of skills/ego-rsi/SKILL.md, so any MCP client can load the loop from the
// server without the repository. Like the tools it is defined once here and served on both transports.
func Prompt() mcp.Prompt {
	return mcp.NewPrompt(RSILoopPrompt,
		mcp.WithPromptDescription("How an agent checks its own quality in ego and corrects itself: read the findings, change yourself, register the correction, wait for ego's verification."))
}

func rsiLoopHandler(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return mcp.NewGetPromptResult("The ego self-correction loop",
		[]mcp.PromptMessage{mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(skills.EgoRSI))}), nil
}

// Tools is the registry: each tool is defined here once, whatever the transport.
func Tools(b Backend, o Options) []server.ServerTool {
	tools := []server.ServerTool{
		{Tool: mcp.NewTool("list_metrics",
			mcp.WithDescription("List all available evaluation metrics with their descriptions and requirements. "+
				"Call this first to discover which metrics you can pass to evaluate_traces."),
		), Handler: listMetricsHandler(b)},
	}

	if o.EvaluateTraces != nil {
		tools = append(tools, server.ServerTool{Tool: mcp.NewTool("evaluate_traces",
			mcp.WithDescription("Evaluate one or more local agent trace files against selected evaluators. "+
				"Loads trace files from disk, converts them to the internal invocation format, and runs each "+
				"requested evaluator. Does not require the ego server to be running."),
			mcp.WithArray("trace_files", mcp.Required(),
				mcp.Description("Absolute paths to trace files on disk. Supports Jaeger JSON and OTLP JSON/JSONL. Each file may contain one or more traces."),
				mcp.Items(map[string]any{"type": "string"})),
			mcp.WithArray("metrics",
				mcp.Description("Metric names to evaluate (from list_metrics). Defaults to [\"tool_trajectory_avg_score\"] if not specified."),
				mcp.Items(map[string]any{"type": "string"})),
			mcp.WithString("trace_format",
				mcp.Description("Optional explicit format override (\"jaeger-json\" or \"otlp-json\"). Auto-detected from file contents when omitted.")),
			mcp.WithString("eval_set_file",
				mcp.Description("Absolute path to a golden eval set JSON file (ADK EvalSet format). Required by comparison metrics such as tool_trajectory_avg_score and response_match_score.")),
			mcp.WithString("judge_model",
				mcp.Description("LLM model name for judge-based metrics, e.g. \"gemini-2.5-flash\". Required by metrics like hallucinations_v1 and final_response_match_v2.")),
			mcp.WithNumber("threshold", mcp.DefaultNumber(0.5),
				mcp.Description("Score threshold for PASS/FAIL classification, between 0.0 and 1.0.")),
			mcp.WithArray("rubrics",
				mcp.Description("Rubric text for rubric_based_final_response_quality_v1/rubric_based_tool_use_quality_v1 (repeatable); required by those metrics."),
				mcp.Items(map[string]any{"type": "string"})),
			mcp.WithString("jev_questions_file",
				mcp.Description("Absolute path to a custom question file for the Jev eval (each question runs as a jev:<key> metric). The jev_* metrics need JEV_API_KEY on this machine.")),
		), Handler: o.EvaluateTraces})
	}

	tools = append(tools,
		server.ServerTool{Tool: mcp.NewTool("list_sessions",
			mcp.WithDescription("List recent streaming trace sessions, ordered most recent first, with the agent, tenant, "+
				"service, error span count and total tokens of each. Use this to discover session IDs for "+
				"summarize_session. All filters are optional and combine; they only narrow what you may see. `query` "+
				"matches the session id, the agent names and the service, and the first user message of sessions that "+
				"have completed (a session still open has no user message yet). Requires the ego server to be running "+
				"(ego serve)."),
			mcp.WithNumber("limit", mcp.DefaultNumber(20),
				mcp.Description("Maximum number of sessions to return.")),
			mcp.WithString("agent",
				mcp.Description("Only sessions in which this exact agent name (gen_ai.agent.name) ran.")),
			mcp.WithString("tenant",
				mcp.Description("Only sessions of this tenant key, or \"<sender>/*\" for every tenant of one sender.")),
			mcp.WithString("since",
				mcp.Description("Only sessions that started at or after this: an RFC 3339 time or a duration such as 24h.")),
			mcp.WithString("query",
				mcp.Description("Case-insensitive text to find in the session id, agent names, service or first user message.")),
			mcp.WithBoolean("complete",
				mcp.Description("true for finished sessions only, false for sessions still open. Omit for both.")),
			mcp.WithBoolean("scored",
				mcp.Description("true for sessions online evaluation has scored (their judge scores can be read with "+
					"explain_session and labelled), false for the others. Omit for both.")),
		), Handler: listSessionsHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("summarize_session",
			mcp.WithDescription("Get a structured summary of a session showing its invocations, tool calls, and messages "+
				"in human-readable form. Use this to understand what an agent did during a session before running "+
				"evaluation. Requires the ego server to be running."),
			mcp.WithString("session_id", mcp.Required(),
				mcp.Description("Session ID obtained from list_sessions.")),
		), Handler: summarizeSessionHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("list_runs",
			mcp.WithDescription("List past evaluation runs (run history), most recent first. Use this to discover run "+
				"IDs for get_run_results. Requires the ego server to be running with --session-db/"+
				"EGO_SESSION_DB_PATH set (run history storage). A tenant-limited caller sees only the online "+
				"evaluation runs of its tenants."),
			mcp.WithNumber("limit", mcp.DefaultNumber(20),
				mcp.Description("Maximum number of runs to return.")),
			mcp.WithArray("status",
				mcp.Description("Optional status filter, e.g. [\"succeeded\"], [\"failed\"], [\"running\"]. Returns all statuses if omitted."),
				mcp.Items(map[string]any{"type": "string"})),
		), Handler: listRunsHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("get_run_results",
			mcp.WithDescription("Get the full result detail for one evaluation run: its status, summary counts, "+
				"per-metric averages, and the per-eval-case/per-evaluator result rows (scores, pass/fail, errors). "+
				"Use this after list_runs to inspect why a run passed or failed. Requires the ego server to "+
				"be running with run history storage enabled."),
			mcp.WithString("run_id", mcp.Required(),
				mcp.Description("Run ID obtained from list_runs.")),
		), Handler: getRunResultsHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("get_usage",
			mcp.WithDescription("Token usage of the LLM calls ego has seen, as the Usage view shows it: input, output and "+
				"total tokens and the call count in the window, the same split by kind (the agent or workflow), tenant, "+
				"model or service with the largest first, and the sessions that used the most tokens. Scoped to the "+
				"tenants you may see; refused to a role limited to agents. Requires run history storage on the server "+
				"(--session-db). No cost is estimated."),
			mcp.WithNumber("hours", mcp.DefaultNumber(defaultUsageHours),
				mcp.Description("Window in hours, 1 to 2160.")),
			mcp.WithString("tenant",
				mcp.Description("Only this tenant key, or \"<sender>/*\" for every tenant of one sender. Omit for all you may see.")),
			mcp.WithString("group", mcp.DefaultString(UsageByKind), mcp.Enum(UsageByKind, UsageByTenant, UsageByModel, UsageByService),
				mcp.Description("What the breakdown groups by.")),
			mcp.WithNumber("top", mcp.DefaultNumber(defaultUsageTop),
				mcp.Description("How many of the largest sessions to list, 1 to 100.")),
		), Handler: getUsageHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("get_quality",
			mcp.WithDescription("How the quality of your agent is doing, in one call: open regression alerts, the hourly "+
				"mean score of every metric, how many sessions were scored, and today's judge budget. Scoped to the "+
				"tenants you may see. Requires online evaluation to be configured on the server."),
			mcp.WithString("kind",
				mcp.Description("Only this call kind (the agent or workflow name). Omit for all kinds.")),
			mcp.WithNumber("hours", mcp.DefaultNumber(defaultQualityHours),
				mcp.Description("Window in hours, 1 to 720.")),
		), Handler: getQualityHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("explain_regression",
			mcp.WithDescription("Explain a quality alert: the alert, the version or model it is attributed to, and its "+
				"worst sessions, each with the score of every metric and the judge's evidence: per-sentence verdicts "+
				"for hallucinations_v1 (unsupported, contradictory and disputed first), per-rubric verdicts for "+
				"rubric metrics, the tool calls for trajectory metrics and the decision for jev_* metrics. Read "+
				"the evidence yourself before trusting any summary."),
			mcp.WithString("alert_id", mcp.Required(),
				mcp.Description("Alert id from get_quality (open_alerts[].alertId).")),
		), Handler: explainRegressionHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("explain_session",
			mcp.WithDescription("The judge's evidence for one scored session: the score of every metric and why, in the "+
				"same shape as the sessions of explain_regression. Use it to look at a session you pick yourself."),
			mcp.WithString("session_id", mcp.Required(),
				mcp.Description("Session ID of an online-scored session, e.g. from list_sessions.")),
		), Handler: explainSessionHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("get_findings",
			mcp.WithDescription("The diagnoses written for your agent's low scores, last updated first: the failure mode, "+
				"a summary, a suggested correction, a confidence, the evidence sessions and the corrections already "+
				"tried (history). A finding is open until a correction names it, then corrected until ego has "+
				"verified it (resolved, or open again when it did not help). Read the evidence with explain_session "+
				"before trusting the summary. Scoped to the tenants you may see."),
			mcp.WithString("kind",
				mcp.Description("Only this call kind. Omit for all kinds.")),
			mcp.WithString("state", mcp.DefaultString(defaultFindingState),
				mcp.Description("open, corrected, resolved, stale, or all.")),
			mcp.WithString("since",
				mcp.Description("Only findings updated since this RFC 3339 time or this long ago (e.g. 24h).")),
		), Handler: getFindingsHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("register_correction",
			mcp.WithDescription("Record what you changed in answer to a finding. The finding becomes corrected, the "+
				"sampler scores your next sessions first, and ego verifies on its detector ticks whether the scores "+
				"recovered: verified resolves the finding, failed reopens it with this attempt in its history. "+
				"Returns the correction with its verification window. Ship the change under a new service.version "+
				"and pass it, so only sessions at that version count."),
			mcp.WithString("finding_id",
				mcp.Description("The finding this change answers (from get_findings). Omit for a change that answers no finding.")),
			mcp.WithString("kind", mcp.Required(),
				mcp.Description("The call kind the change applies to.")),
			mcp.WithString("what", mcp.Required(),
				mcp.Description("What you changed, in plain words (at most 2000 characters).")),
			mcp.WithString("version",
				mcp.Description("The service.version the change ships as.")),
			mcp.WithString("by",
				mcp.Description("Who made the change: an agent name or a person.")),
			mcp.WithString("tenant",
				mcp.Description("The tenant. Implied by the finding, or by a token limited to one tenant.")),
		), Handler: registerCorrectionHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("label_session",
			mcp.WithDescription("Record a person's verdict on a judge's score: agree or disagree with the online score of "+
				"one metric on one session, with the score you think is right and a note. The label is the score of "+
				"record: a score you give replaces the judge's in every mean, alert, finding and verification; an "+
				"agreement confirms the judge's score; a disagreement without a score drops the session from them. "+
				"Findings cite checked sessions first, with the note. ego also measures how often people agree with "+
				"each metric's judge; a metric whose judge people mostly disagree with stops producing findings. "+
				"Labels are for people reading the evidence: do not label sessions you produced. Fails when the "+
				"session has no online score for the metric."),
			mcp.WithString("session_id", mcp.Required(),
				mcp.Description("An online-scored session.")),
			mcp.WithString("metric", mcp.Required(),
				mcp.Description("The metric whose score you judge, as explain_session lists it.")),
			mcp.WithString("verdict", mcp.Required(),
				mcp.Enum("agree", "disagree"),
				mcp.Description("agree or disagree with the judge's score.")),
			mcp.WithNumber("score",
				mcp.Description("The score you think is right, between 0 and 1. Give one with a disagreement, or the "+
					"session counts nowhere until someone does.")),
			mcp.WithString("note",
				mcp.Description("Why, in a sentence (at most 1000 characters).")),
		), Handler: labelSessionHandler(b)},

		server.ServerTool{Tool: mcp.NewTool("list_corrections",
			mcp.WithDescription("The corrections registered for your tenants, newest first, each with its verification: the "+
				"mean score before and after, how many sessions, and when the window ends. State is pending, verified "+
				"or failed."),
			mcp.WithString("kind",
				mcp.Description("Only this call kind. Omit for all kinds.")),
			mcp.WithString("state",
				mcp.Description("pending, verified, failed, or all (the default).")),
		), Handler: listCorrectionsHandler(b)},
	)

	if o.Admin {
		tools = append(tools,
			server.ServerTool{Tool: mcp.NewTool("list_role_bindings",
				mcp.WithDescription("List all admin/member/viewer role bindings (multi-user RBAC). Requires the caller "+
					"to be an admin, and the server to be running with --session-secret (GitHub OAuth) enabled."),
			), Handler: listRoleBindingsHandler(b)},

			server.ServerTool{Tool: mcp.NewTool("set_role_binding",
				mcp.WithDescription("Create or update a role binding: grants a role (admin/member/viewer), optionally "+
					"scoped to specific agent names, to a GitHub user or team. Pass id to update an existing binding "+
					"in place; omit it to create a new one. Requires the caller to be an admin."),
				mcp.WithString("subject_type", mcp.Required(),
					mcp.Description("\"user\" or \"team\".")),
				mcp.WithString("subject", mcp.Required(),
					mcp.Description("GitHub login for subject_type \"user\" (e.g. \"octocat\"), or \"org/team-slug\" for subject_type \"team\" (e.g. \"my-org/my-team\").")),
				mcp.WithString("role", mcp.Required(),
					mcp.Description("Role to grant: \"admin\", \"member\", or \"viewer\".")),
				mcp.WithArray("agents",
					mcp.Description("Optional agent-name allowlist scoping this binding to Runs for only those agents. Omit/empty for unrestricted."),
					mcp.Items(map[string]any{"type": "string"})),
				mcp.WithArray("tenants",
					mcp.Description("Optional tenant allowlist: the subject sees only data of those tenants (senders with their own ingest token). Omit/empty for every tenant."),
					mcp.Items(map[string]any{"type": "string"})),
				mcp.WithString("id",
					mcp.Description("ID of an existing role binding to update in place (from list_role_bindings). Omit to create a new binding.")),
			), Handler: setRoleBindingHandler(b)},

			server.ServerTool{Tool: mcp.NewTool("run_findings",
				mcp.WithDescription("Run the findings sweep now instead of waiting for findings.every: every series with "+
					"enough low-scoring sessions in the window and no open finding gets a diagnosis. Spends judge budget like "+
					"a scheduled sweep. Answers what the sweep did. Requires the caller to be an admin."),
			), Handler: runFindingsHandler(b)},

			server.ServerTool{Tool: mcp.NewTool("delete_role_binding",
				mcp.WithDescription("Delete a role binding by ID (from list_role_bindings). Requires the caller to be an admin."),
				mcp.WithString("id", mcp.Required(),
					mcp.Description("ID of the role binding to delete.")),
			), Handler: deleteRoleBindingHandler(b)},
		)
	}
	return tools
}

func toolError(err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(err.Error()), nil
}

func listMetricsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := b.Metrics(ctx)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func listSessionsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit := req.GetInt("limit", 20)
		since, err := ParseSince(req.GetString("since", ""), time.Now())
		if err != nil {
			return toolError(err)
		}
		filter := SessionFilter{
			Agent:  strings.TrimSpace(req.GetString("agent", "")),
			Tenant: strings.TrimSpace(req.GetString("tenant", "")),
			Since:  since,
			Query:  strings.TrimSpace(req.GetString("query", "")),
		}
		// A bool argument is only a filter when it was given: false means "still open", absent means both.
		if _, given := req.GetArguments()["complete"]; given {
			c := req.GetBool("complete", false)
			filter.Complete = &c
		}
		if _, given := req.GetArguments()["scored"]; given {
			c := req.GetBool("scored", false)
			filter.Scored = &c
		}
		out, err := b.Sessions(ctx, filter)
		if err != nil {
			return toolError(err)
		}
		// Most recent first; ISO 8601 timestamps sort correctly as plain strings.
		sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
		if limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		if out == nil {
			out = []SessionListItem{}
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func getUsageHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := UsageQuery{
			Hours:  req.GetInt("hours", defaultUsageHours),
			Tenant: strings.TrimSpace(req.GetString("tenant", "")),
			Group:  req.GetString("group", UsageByKind),
			Top:    req.GetInt("top", defaultUsageTop),
		}
		if q.Hours < 1 || q.Hours > maxUsageHours {
			return toolError(fmt.Errorf("hours must be between 1 and %d", maxUsageHours))
		}
		if q.Top < 1 || q.Top > maxUsageTop {
			return toolError(fmt.Errorf("top must be between 1 and %d", maxUsageTop))
		}
		switch q.Group {
		case UsageByKind, UsageByTenant, UsageByModel, UsageByService:
		default:
			return toolError(fmt.Errorf("group must be one of kind, tenant, model, service"))
		}
		out, err := b.Usage(ctx, q)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func summarizeSessionHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID, err := req.RequireString("session_id")
		if err != nil {
			return toolError(err)
		}
		traces, numSpans, err := b.SessionTraces(ctx, sessionID)
		if err != nil {
			return toolError(err)
		}
		out := SessionSummary{SessionID: sessionID, NumSpans: numSpans, Invocations: []InvocationSummary{}}
		for _, tr := range traces {
			conv := adk.ConvertTrace(tr)
			for _, inv := range conv.Invocations {
				calls := []ToolCall{}
				for _, tc := range inv.ToolCalls() {
					args := tc.Args
					if args == nil {
						args = map[string]any{}
					}
					calls = append(calls, ToolCall{Tool: tc.Name, Args: args})
				}
				out.Invocations = append(out.Invocations, InvocationSummary{
					User:      inv.UserText(),
					Response:  inv.FinalResponse.Text(),
					ToolCalls: calls,
				})
			}
		}
		out.NumInvocations = len(out.Invocations)
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func listRunsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := b.Runs(ctx, req.GetInt("limit", 20), req.GetStringSlice("status", nil))
		if err != nil {
			return toolError(err)
		}
		if out == nil {
			out = []RunListItem{}
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func getRunResultsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		runID, err := req.RequireString("run_id")
		if err != nil {
			return toolError(err)
		}
		out, err := b.RunResults(ctx, runID)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func getQualityHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hours := req.GetInt("hours", defaultQualityHours)
		if hours < 1 || hours > maxQualityHours {
			return toolError(fmt.Errorf("hours must be between 1 and %d", maxQualityHours))
		}
		out, err := b.Quality(ctx, req.GetString("kind", ""), hours)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func explainRegressionHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("alert_id")
		if err != nil {
			return toolError(err)
		}
		out, err := b.ExplainRegression(ctx, id)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func explainSessionHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolError(err)
		}
		out, err := b.ExplainSession(ctx, id)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func getFindingsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		since, err := ParseSince(req.GetString("since", ""), time.Now())
		if err != nil {
			return toolError(err)
		}
		out, err := b.Findings(ctx, req.GetString("kind", ""), req.GetString("state", defaultFindingState), since)
		if err != nil {
			return toolError(err)
		}
		if out == nil {
			out = []online.Finding{}
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func registerCorrectionHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kind, err := req.RequireString("kind")
		if err != nil {
			return toolError(err)
		}
		what, err := req.RequireString("what")
		if err != nil {
			return toolError(err)
		}
		out, err := b.RegisterCorrection(ctx, CorrectionInput{
			FindingID: req.GetString("finding_id", ""),
			Tenant:    req.GetString("tenant", ""),
			Kind:      kind,
			What:      what,
			Version:   req.GetString("version", ""),
			By:        req.GetString("by", ""),
		})
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func labelSessionHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID, err := req.RequireString("session_id")
		if err != nil {
			return toolError(err)
		}
		metric, err := req.RequireString("metric")
		if err != nil {
			return toolError(err)
		}
		verdict, err := req.RequireString("verdict")
		if err != nil {
			return toolError(err)
		}
		in := LabelInput{SessionID: sessionID, Metric: metric, Verdict: verdict, Note: req.GetString("note", "")}
		if _, given := req.GetArguments()["score"]; given {
			score := req.GetFloat("score", 0)
			in.Score = &score
		}
		out, err := b.LabelSession(ctx, in)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func listCorrectionsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := b.Corrections(ctx, req.GetString("kind", ""), req.GetString("state", ""))
		if err != nil {
			return toolError(err)
		}
		if out == nil {
			out = []online.Correction{}
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func runFindingsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := b.RunFindings(ctx)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func listRoleBindingsHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := b.RoleBindings(ctx)
		if err != nil {
			return toolError(err)
		}
		if out == nil {
			out = []RoleBinding{}
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func setRoleBindingHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		subjectType, err := req.RequireString("subject_type")
		if err != nil {
			return toolError(err)
		}
		subject, err := req.RequireString("subject")
		if err != nil {
			return toolError(err)
		}
		role, err := req.RequireString("role")
		if err != nil {
			return toolError(err)
		}
		out, err := b.SetRoleBinding(ctx, RoleBindingInput{
			ID:          req.GetString("id", ""),
			SubjectType: subjectType,
			Subject:     subject,
			Role:        role,
			Agents:      req.GetStringSlice("agents", nil),
			Tenants:     req.GetStringSlice("tenants", nil),
		})
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}

func deleteRoleBindingHandler(b Backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return toolError(err)
		}
		out, err := b.DeleteRoleBinding(ctx, id)
		if err != nil {
			return toolError(err)
		}
		return mcp.NewToolResultStructuredOnly(out), nil
	}
}
