// Command ego scores OpenTelemetry agent traces against a golden eval set,
// without re-executing the agent. See README.md and docs/STATUS.md for what
// is supported.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/api"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/eval"
	"github.com/triageagent-dev/ego/internal/judge"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
	"github.com/triageagent-dev/ego/internal/vertexeval"
)

// judgeMetrics are the metric names the CLI routes to internal/judge instead of eval.RunMetrics, since they
// need a live judge model call (context, API key) that the local-only eval package deliberately has no
// dependency on.
var judgeMetrics = map[string]bool{
	"final_response_match_v2":                true,
	"hallucinations_v1":                      true,
	"rubric_based_final_response_quality_v1": true,
	"rubric_based_tool_use_quality_v1":       true,
}

// vertexEvalMetrics are the metric names the CLI routes to
// internal/vertexeval (Vertex AI's Managed Eval Service) instead of
// internal/judge or eval.RunMetrics - architecturally distinct from
// judgeMetrics, see internal/vertexeval's package doc.
var vertexEvalMetrics = map[string]bool{
	"safety_v1":                        true,
	"multi_turn_task_success_v1":       true,
	"multi_turn_trajectory_quality_v1": true,
	"multi_turn_tool_use_quality_v1":   true,
}

// runJudgeMetric dispatches one judgeMetrics-listed metric name to its
// internal/judge implementation. Mirrors internal/api/evaluate.go's
// runJudgeMetric.
func runJudgeMetric(ctx context.Context, name string, model judge.Model, actual, expected []adk.Invocation, rubrics []judge.Rubric, numSamples int, threshold float64) (eval.Result, error) {
	switch name {
	case "final_response_match_v2":
		if expected == nil {
			return eval.Result{}, fmt.Errorf("metric %q requires --eval-set (golden eval set)", name)
		}
		return judge.FinalResponseMatchV2(ctx, model, actual, expected, numSamples, threshold)
	case "hallucinations_v1":
		return judge.HallucinationsV1(ctx, model, actual, threshold)
	case "rubric_based_final_response_quality_v1":
		if len(rubrics) == 0 {
			return eval.Result{}, fmt.Errorf("metric %q requires at least one --rubric", name)
		}
		return judge.RubricBasedFinalResponseQualityV1(ctx, model, actual, rubrics, numSamples, threshold)
	case "rubric_based_tool_use_quality_v1":
		if len(rubrics) == 0 {
			return eval.Result{}, fmt.Errorf("metric %q requires at least one --rubric", name)
		}
		return judge.RubricBasedToolUseQualityV1(ctx, model, actual, rubrics, numSamples, threshold)
	default:
		return eval.Result{}, fmt.Errorf("unknown judge metric %q", name)
	}
}

// runVertexEvalMetric dispatches one vertexEvalMetrics-listed metric name
// to its internal/vertexeval implementation. Mirrors
// internal/api/evaluate.go's runVertexEvalMetric.
func runVertexEvalMetric(ctx context.Context, name string, client *vertexeval.Client, actual, expected []adk.Invocation, threshold float64) (eval.Result, error) {
	switch name {
	case "safety_v1":
		return vertexeval.SafetyV1(ctx, client, actual, expected, threshold)
	case "multi_turn_task_success_v1":
		return vertexeval.MultiTurnTaskSuccessV1(ctx, client, actual, threshold)
	case "multi_turn_trajectory_quality_v1":
		return vertexeval.MultiTurnTrajectoryQualityV1(ctx, client, actual, threshold)
	case "multi_turn_tool_use_quality_v1":
		return vertexeval.MultiTurnToolUseQualityV1(ctx, client, actual, threshold)
	default:
		return eval.Result{}, fmt.Errorf("unknown vertex-eval metric %q", name)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "serve":
		err = serveCmd(os.Args[2:])
	case "auth":
		err = authCmd(os.Args[2:])
	case "mcp":
		err = mcpCmd(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("ego", api.Version)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "ego: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "ego: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ego - reality testing for AI agents: score OTel agent traces against a golden eval set.

Usage:
  ego run <trace-file>... --eval-set <path> -m <metric> [-m <metric>...] [flags]
  ego serve [--addr :8001] [--otlp-addr :4318] [--otlp-grpc-addr :4317] [--health-addr :9090] [--session-db path] [--session-memory n] [--session-retention 720h] [--usage-tenant-attr key] [--session-secret secret] [--otlp-token token] [--eval-daily-tokens n]
                   [--admin-github-users user1,user2] [--default-role member]
  ego auth mint-token --name <label> [--ttl-days 90] [--secret ...]
  ego mcp [--server-url http://localhost:8001] [--session-token ...]
  ego version

Run flags:
  --eval-set string            golden eval set JSON (required for tool_trajectory_avg_score, response_match_score,
                                final_response_match_v2)
  -m, --metric value            metric to run (repeatable); default: tool_trajectory_avg_score
  --trajectory-match-type value EXACT | IN_ORDER | ANY_ORDER (default EXACT)
  --threshold float              pass/fail threshold applied to every metric (default 0.5)
  --output string                 text | json (default text)
  --judge-api-key string          Gemini API key for judge-model metrics (falls back to GEMINI_API_KEY/GOOGLE_API_KEY,
                                    or ADC/Vertex if GOOGLE_GENAI_USE_VERTEXAI=true - no key needed)
  --judge-model string             judge model for judge-model metrics (default gemini-2.5-flash)
  --judge-samples int               samples per invocation for judge-model metrics, majority-voted (default 5)
  --rubric string                  rubric text for rubric_based_*_v1 metrics (repeatable); required by those metrics
  --jev-questions string           custom question file for the Jev eval; each question runs as a jev:<key> metric
  --jev-api-key string             API key for jev_* metrics (falls back to JEV_API_KEY)
  --jev-model string               Jev model for jev_* metrics (falls back to JEV_MODEL, default jev-1.13)

Metrics needing a live judge model (final_response_match_v2, hallucinations_v1, rubric_based_final_response_quality_v1,
rubric_based_tool_use_quality_v1) call Gemini directly. Metrics needing Vertex AI's Managed Eval Service (safety_v1,
multi_turn_task_success_v1, multi_turn_trajectory_quality_v1, multi_turn_tool_use_quality_v1) read
GOOGLE_CLOUD_PROJECT/GOOGLE_CLOUD_LOCATION and use Application Default Credentials - no API key, ever.

Jev metrics (jev_task_resolved, jev_grounded, jev_tool_use_appropriate, jev_matches_reference [needs --eval-set],
jev_response_kind, and custom questions via --jev-questions) send each invocation to the Jev decision model as a
structured state with typed questions, one call per invocation for all of them. A metric's score is the mean
probability Jev gives the passing answer.

Trace files: Jaeger JSON, OTLP JSON and JSONL, with ADK, GenAI-semconv or OpenInference attributes.
See docs/STATUS.md for what is supported.

MCP: ego mcp starts a Model Context Protocol server on stdio (list_metrics,
evaluate_traces, list_sessions, summarize_session) for use with Claude Code, Cursor, GitHub Copilot
CLI, etc. list_sessions/summarize_session call --server-url/EGO_SERVER_URL (a running
'ego serve'); pass --session-token/EGO_SESSION_TOKEN (from 'ego auth
mint-token') if that server has GitHub OAuth enabled. evaluate_traces is fully offline.
`)
}

type traceReport struct {
	TraceID string        `json:"trace_id"`
	Results []eval.Result `json:"results"`
}

type metricFlags []string

func (m *metricFlags) String() string { return strings.Join(*m, ",") }
func (m *metricFlags) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// runFlagsWithValue lists every "run" flag that consumes the following
// token as its value (none of them are boolean). Used by splitPositional
// to pull trace-file paths out regardless of where they fall relative to
// the flags, since Go's flag package (unlike click/argparse) stops parsing
// flags at the first non-flag argument.
var runFlagsWithValue = map[string]bool{
	"eval-set":              true,
	"m":                     true,
	"metric":                true,
	"trajectory-match-type": true,
	"threshold":             true,
	"output":                true,
	"judge-api-key":         true,
	"judge-model":           true,
	"judge-samples":         true,
	"rubric":                true,
	"jev-questions":         true,
	"jev-api-key":           true,
	"jev-model":             true,
}

// splitPositional separates positional arguments (trace file paths) from
// flag tokens, so `ego run trace.json --eval-set set.json` and
// `ego run --eval-set set.json trace.json` both work.
func splitPositional(args []string, flagsWithValue map[string]bool) (positional, flagArgs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue // value is embedded in this token (--flag=value)
		}
		if flagsWithValue[name] && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return positional, flagArgs
}

func runCmd(args []string) error {
	traceFiles, flagArgs := splitPositional(args, runFlagsWithValue)

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	evalSetPath := fs.String("eval-set", "", "golden eval set JSON path")
	var metrics metricFlags
	fs.Var(&metrics, "m", "metric to run (repeatable)")
	fs.Var(&metrics, "metric", "metric to run (repeatable)")
	matchTypeFlag := fs.String("trajectory-match-type", "EXACT", "EXACT | IN_ORDER | ANY_ORDER")
	threshold := fs.Float64("threshold", 0.5, "pass/fail threshold")
	output := fs.String("output", "text", "text | json")
	judgeAPIKey := fs.String("judge-api-key", "", "Gemini API key for judge-model metrics; falls back to GEMINI_API_KEY/GOOGLE_API_KEY, or ADC/Vertex if GOOGLE_GENAI_USE_VERTEXAI=true")
	judgeModel := fs.String("judge-model", judge.DefaultModel, "judge model for judge-model metrics")
	judgeSamples := fs.Int("judge-samples", judge.DefaultNumSamples, "samples per invocation for judge-model metrics, aggregated by majority vote")
	var rubrics metricFlags
	fs.Var(&rubrics, "rubric", "rubric text for rubric_based_*_v1 metrics (repeatable); required by those metrics")
	jevQuestions := fs.String("jev-questions", "", "custom question file for the Jev eval (runs as jev:<key> metrics)")
	jevAPIKey := fs.String("jev-api-key", "", "API key for jev_* metrics; falls back to JEV_API_KEY")
	jevModel := fs.String("jev-model", "", "Jev model for jev_* metrics; falls back to JEV_MODEL, default "+decision.DefaultModel)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if len(traceFiles) == 0 {
		return fmt.Errorf("at least one trace file is required")
	}
	if len(metrics) == 0 && *jevQuestions == "" {
		metrics = metricFlags{"tool_trajectory_avg_score"}
	}

	matchType, err := eval.ParseMatchType(*matchTypeFlag)
	if err != nil {
		return err
	}

	var evalSet *adk.EvalSet
	if *evalSetPath != "" {
		evalSet, err = eval.LoadEvalSet(*evalSetPath)
		if err != nil {
			return fmt.Errorf("loading eval set: %w", err)
		}
	}

	thresholds := make(map[string]float64, len(metrics))
	for _, m := range metrics {
		thresholds[m] = *threshold
	}

	var localMetrics, requestedJudgeMetrics, requestedVertexMetrics, requestedJevMetrics []string
	for _, m := range metrics {
		switch {
		case decision.IsMetric(m):
			requestedJevMetrics = append(requestedJevMetrics, m)
		case judgeMetrics[m]:
			requestedJudgeMetrics = append(requestedJudgeMetrics, m)
		case vertexEvalMetrics[m]:
			requestedVertexMetrics = append(requestedVertexMetrics, m)
		default:
			localMetrics = append(localMetrics, m)
		}
	}

	var judgeModelClient judge.Model
	if len(requestedJudgeMetrics) > 0 {
		client, err := judge.NewGenAIModel(context.Background(), *judgeAPIKey, *judgeModel)
		if err != nil {
			return fmt.Errorf("setting up judge model: %w", err)
		}
		judgeModelClient = client
	}

	var vertexEvalClient *vertexeval.Client
	if len(requestedVertexMetrics) > 0 {
		client, err := vertexeval.NewClient(context.Background())
		if err != nil {
			return fmt.Errorf("setting up Vertex AI Managed Eval Service client: %w", err)
		}
		vertexEvalClient = client
	}

	rubricObjects := judge.RubricsFromStrings(rubrics)

	var jevRequests []decision.Request
	var jevClient *decision.Client
	if len(requestedJevMetrics) > 0 || *jevQuestions != "" {
		var custom []byte
		if *jevQuestions != "" {
			if custom, err = os.ReadFile(*jevQuestions); err != nil {
				return fmt.Errorf("reading jev questions: %w", err)
			}
		}
		if jevRequests, err = decision.Requests(requestedJevMetrics, custom, *threshold); err != nil {
			return err
		}
		if jevClient, err = decision.NewClient(*jevAPIKey, *jevModel); err != nil {
			return err
		}
	}

	var reports []traceReport
	failures := 0

	for _, path := range traceFiles {
		traces, err := tracepkg.LoadJaegerJSON(path)
		if err != nil {
			return fmt.Errorf("loading %s: %w", path, err)
		}
		for _, tr := range traces {
			conv := adk.ConvertADKTrace(tr)
			for _, w := range conv.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}

			var results []eval.Result
			if len(localMetrics) > 0 {
				results = append(results, eval.RunMetrics(conv.Invocations, evalSet, localMetrics, matchType, thresholds)...)
			}

			expected := eval.FindExpectedInvocations(conv.Invocations, evalSet)
			for _, m := range requestedJudgeMetrics {
				result, err := runJudgeMetric(context.Background(), m, judgeModelClient, conv.Invocations, expected, rubricObjects, *judgeSamples, thresholds[m])
				if err != nil {
					return fmt.Errorf("running %s: %w", m, err)
				}
				results = append(results, result)
			}
			for _, m := range requestedVertexMetrics {
				result, err := runVertexEvalMetric(context.Background(), m, vertexEvalClient, conv.Invocations, expected, thresholds[m])
				if err != nil {
					return fmt.Errorf("running %s: %w", m, err)
				}
				results = append(results, result)
			}
			if len(jevRequests) > 0 {
				jevResults, err := decision.Evaluate(context.Background(), jevClient, conv.Invocations, expected, jevRequests)
				if err != nil {
					return fmt.Errorf("running jev metrics: %w", err)
				}
				results = append(results, jevResults...)
			}

			for _, r := range results {
				if r.Error != "" || r.Status == eval.StatusFailed {
					failures++
				}
			}
			reports = append(reports, traceReport{TraceID: tr.TraceID, Results: results})
		}
	}

	if *output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			return err
		}
	} else {
		printTextReport(reports)
	}

	if failures > 0 {
		os.Exit(1)
	}
	return nil
}

func printTextReport(reports []traceReport) {
	for _, r := range reports {
		fmt.Printf("Trace: %s\n", r.TraceID)
		for _, res := range r.Results {
			if res.Error != "" {
				fmt.Printf("[ERROR] %-30s %s\n", res.MetricName, res.Error)
				continue
			}
			status := "PASS"
			if res.Status == eval.StatusFailed {
				status = "FAIL"
			}
			fmt.Printf("[%s]  %-30s score=%.2f status=%s\n", status, res.MetricName, res.Score, res.Status)
		}
		fmt.Println()
	}
}

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8001", "listen address for the REST API and UI (behind auth when --session-secret is set)")
	otlpAddr := fs.String("otlp-addr", ":4318", "listen address for the OTLP/HTTP trace receiver (never gated)")
	otlpGRPCAddr := fs.String("otlp-grpc-addr", ":4317", "listen address for the OTLP/gRPC trace receiver (never gated)")
	healthAddr := fs.String("health-addr", ":9090", "listen address for GET /api/health (its own port, always unauthenticated - see internal/api/server.go's Serve doc comment for why)")
	sessionDB := fs.String("session-db", env("SESSION_DB_PATH"),
		"SQLite file to persist streaming sessions across restarts (default: none, in-memory only); falls back to EGO_SESSION_DB_PATH")
	sessionSecret := fs.String("session-secret", env("SESSION_SECRET"),
		"HMAC secret used to sign/verify the ego_session cookie (default: none, unauthenticated); falls back to EGO_SESSION_SECRET. Required (along with --github-client-id/--github-client-secret/--github-org/--public-url) to enable this service's own independent GitHub OAuth login flow; see internal/api/oauth.go.")
	githubClientID := fs.String("github-client-id", env("GITHUB_CLIENT_ID"),
		"GitHub OAuth App client ID; falls back to EGO_GITHUB_CLIENT_ID. Setting this and --github-client-secret enables this service's own /auth/login, /auth/callback and /auth/logout - no other process is needed to log a user in.")
	githubClientSecret := fs.String("github-client-secret", env("GITHUB_CLIENT_SECRET"),
		"GitHub OAuth App client secret; falls back to EGO_GITHUB_CLIENT_SECRET.")
	githubOrg := fs.String("github-org", env("GITHUB_ORG"),
		"GitHub org whose active members are granted access once OAuth is enabled; falls back to EGO_GITHUB_ORG. Required whenever --github-client-id/--github-client-secret are set.")
	publicURL := fs.String("public-url", env("PUBLIC_URL"),
		"this service's own externally-reachable base URL, e.g. https://your-app.example.com, used to build an exact OAuth redirect_uri (PublicURL+\"/auth/callback\"); falls back to EGO_PUBLIC_URL. Required whenever --github-client-id/--github-client-secret are set (no fallback is derived from the incoming request - see internal/api/oauth.go's package doc).")
	adminGitHubUsers := fs.String("admin-github-users", env("ADMIN_GITHUB_USERS"),
		"comma-separated GitHub logins idempotently granted the admin role at startup (see internal/api/roles.go); falls back to EGO_ADMIN_GITHUB_USERS. Only takes effect when --session-secret is set. The only way to get a first admin into an otherwise-empty role store - after that, admins manage every role (including further admins) via GET/POST /api/admin/roles.")
	defaultRole := fs.String("default-role", envOrDefault("DEFAULT_ROLE", "member"),
		"role (admin | member | viewer) granted to any authenticated user with no matching role binding; falls back to EGO_DEFAULT_ROLE. \"member\" (the default) gives full read/write access to a deployment that never configures a role binding.")
	sessionMemory := fs.Int("session-memory", envIntOrDefault("SESSION_MEMORY", api.DefaultSessionMemory),
		"completed sessions kept in memory with --session-db; older ones stay in the archive and load on demand (0 = no cap); falls back to EGO_SESSION_MEMORY")
	sessionRetention := fs.Duration("session-retention", envDurationOrDefault("SESSION_RETENTION", api.DefaultSessionRetention),
		"delete archived sessions not updated for this long, once a day, then compact the file (0 = keep forever); falls back to EGO_SESSION_RETENTION")
	usageTenantAttr := fs.String("usage-tenant-attr", env("USAGE_TENANT_ATTR"),
		"span attribute the token usage view reads the tenant from (e.g. tenant.id); empty records no tenant; falls back to EGO_USAGE_TENANT_ATTR")
	evalDailyTokens := fs.Int64("eval-daily-tokens", envInt64OrDefault("EVAL_DAILY_TOKENS", api.DefaultEvalDailyToks),
		"judge tokens the interactive evaluation endpoints may use per UTC day, shared by all callers; 0 means unlimited; falls back to EGO_EVAL_DAILY_TOKENS")
	otlpToken := fs.String("otlp-token", env("OTLP_TOKEN"),
		"bearer token the OTLP/HTTP and OTLP/gRPC receivers require from senders (Authorization: Bearer ...); empty leaves them open; at least 32 bytes; falls back to EGO_OTLP_TOKEN")
	onlineConfig := fs.String("online-config", env("ONLINE_CONFIG"),
		"JSON file that turns on online evaluation: sampling, per-kind evaluator profiles, daily judge caps, regression detection and notification sinks (see docs/online-evaluation.md); needs --session-db; falls back to EGO_ONLINE_CONFIG")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkSessionSecret(*sessionSecret); err != nil {
		return err
	}
	if err := checkSecret("OTLP token", *otlpToken); err != nil {
		return err
	}
	githubOAuth, err := api.GitHubOAuthConfigFromParams(*githubClientID, *githubClientSecret, *githubOrg, *sessionSecret, *publicURL)
	if err != nil {
		return fmt.Errorf("github OAuth config: %w", err)
	}
	var adminUsers []string
	for _, u := range strings.Split(*adminGitHubUsers, ",") {
		if u = strings.TrimSpace(u); u != "" {
			adminUsers = append(adminUsers, u)
		}
	}
	return api.Serve(*addr, *otlpAddr, *otlpGRPCAddr, *healthAddr, *sessionDB, *sessionSecret, githubOAuth, adminUsers, *defaultRole,
		api.SessionLimits{Memory: *sessionMemory, Retention: *sessionRetention, UsageTenantAttr: *usageTenantAttr, EvalDailyTokens: *evalDailyTokens}, *onlineConfig, *otlpToken)
}

// envIntOrDefault reads an integer env var, falling back to def when it is
// unset or not a number.
func envIntOrDefault(key string, def int) int {
	if v, err := strconv.Atoi(env(key)); err == nil {
		return v
	}
	return def
}

// envDurationOrDefault reads a Go duration env var (e.g. "720h"), falling
// back to def when it is unset or invalid.
func envDurationOrDefault(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(env(key)); err == nil {
		return v
	}
	return def
}

// envOrDefault returns env(key) if set (even to an explicit
// override a user wants to clear back to an empty string isn't
// representable this way, but no flag here needs that), else fallback -
// used where a flag's default should be an env var if present, a fixed
// literal otherwise, unlike the plain env(...) pattern used
// elsewhere in this file whose own zero value ("") is already the right
// default.
func envOrDefault(key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}
