// mcp.go implements `ego mcp`: a Model Context Protocol server on stdio, for use with Claude Code, Cursor,
// GitHub Copilot CLI, and other MCP clients.
//
// Two ways to supply it, checked in this order: --session-token/EGO_SESSION_TOKEN (an ego-minted token from
// `ego auth mint-token`), or, if neither is set, a fallback to running `gh auth token` and sending that GitHub
// token straight through - internal/api's requireSession accepts a live GitHub token as an alternative to its
// own signed tokens (see internal/api/githubtoken.go), precisely so this needs no separately minted secret at
// all when the caller already has `gh` authenticated as a member of the deployment's GitHub org.
// evaluate_traces needs no server and no token at all: it loads trace files from local disk and evaluates them
// in-process, exactly like `ego run`. Every other tool is defined in internal/mcptools, shared with the MCP
// endpoint of `serve`, and reaches the server through its REST API.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/eval"
	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/loader"
	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/vertexeval"
)

const defaultMCPServerURL = "http://localhost:8001"

func mcpCmd(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	serverURL := fs.String("server-url", env("SERVER_URL"),
		"ego server URL for every tool except evaluate_traces (default: http://localhost:8001); falls back to EGO_SERVER_URL")
	sessionToken := fs.String("session-token", env("SESSION_TOKEN"),
		"bearer token sent as an Authorization: Bearer header on every request to --server-url, for a GitHub-OAuth-gated deployment; falls back to EGO_SESSION_TOKEN, then to `gh auth token` if neither is set and gh is installed and logged in. Mint one with: ego auth mint-token --name <label>, or just use gh auth login instead.")
	traceDir := fs.String("trace-dir", env("MCP_TRACE_DIR"),
		"directory evaluate_traces may read trace, eval set and question files from (default: the current directory); paths outside it, including through symlinks, are refused; falls back to EGO_MCP_TRACE_DIR")
	adminTools := fs.Bool("admin-tools", false, "register the role-binding tools (list_role_bindings, set_role_binding, delete_role_binding); off by default so a prompt-injected client cannot change access with your credential")
	noGHAuth := fs.Bool("no-gh-auth", false, "disable the gh auth token fallback; fail with no credential instead of shelling out to gh")
	if err := fs.Parse(args); err != nil {
		return err
	}

	url := strings.TrimRight(*serverURL, "/")
	if url == "" {
		url = defaultMCPServerURL
	}

	token := *sessionToken
	if token == "" && !*noGHAuth {
		if tokenSafeFor(url) {
			token = ghAuthToken()
		} else {
			fmt.Fprintf(os.Stderr, "ego mcp: not sending the gh auth token to %s (it is neither https nor a loopback address); pass --session-token to authenticate there\n", url)
		}
	}

	root := *traceDir
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolving the trace directory: %w", err)
		}
		root = cwd
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolving --trace-dir: %w", err)
	}

	backend := mcptools.NewREST(url, token, &http.Client{Timeout: 60 * time.Second})

	s := newMCPServer(backend, mcpOptions{root: root, adminTools: *adminTools})
	return server.ServeStdio(s)
}

// ghAuthToken shells out to `gh auth token` as a zero-config fallback
// credential for a GitHub-OAuth-gated deployment: if the caller already
// has the GitHub CLI authenticated (gh auth login), that token is an
// active member's real GitHub token, which internal/api/githubtoken.go
// accepts directly - no ego-specific secret to mint or store
// anywhere. Best-effort: gh not being installed, not being logged in, or
// exiting non-zero for any other reason all just mean "no token", exactly
// as if --session-token/EGO_SESSION_TOKEN had been left unset;
// this never fails mcpCmd outright - a request to an auth-gated
// deployment then simply fails at the HTTP layer with a normal "not
// authenticated" error, same as always having had no token at all.
func ghAuthToken() string {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mcpOptions are the choices that limit what an MCP client can reach. root confines the files evaluate_traces reads;
// adminTools registers the role-binding tools, which act with the caller's own credential.
type mcpOptions struct {
	root       string
	adminTools bool
}

// newMCPServer builds the stdio server: the shared tool registry over a REST backend, plus evaluate_traces, which
// reads local files and so exists only here. Split out from mcpCmd so tests can call it without stdio.
func newMCPServer(backend mcptools.Backend, opts mcpOptions) *server.MCPServer {
	return mcptools.NewServer(backend, mcptools.Options{
		Admin:          opts.adminTools,
		EvaluateTraces: evaluateTracesHandler(opts.root),
	})
}

// ---------------------------------------------------------------------------
// evaluate_traces (fully offline - no HTTP call)
// ---------------------------------------------------------------------------

type metricScoreMCP struct {
	Metric string   `json:"metric"`
	Score  *float64 `json:"score,omitempty"`
	Status string   `json:"status"`
	Error  string   `json:"error,omitempty"`
}

type traceEvalMCP struct {
	TraceID        string           `json:"trace_id"`
	NumInvocations int              `json:"num_invocations"`
	Metrics        []metricScoreMCP `json:"metrics"`
	Warnings       []string         `json:"warnings,omitempty"`
}

type evaluateTracesResultMCP struct {
	Passed bool           `json:"passed"`
	Traces []traceEvalMCP `json:"traces"`
	Errors []string       `json:"errors,omitempty"`
}

func evaluateTracesHandler(root string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		traceFiles := req.GetStringSlice("trace_files", nil)
		if len(traceFiles) == 0 {
			return mcp.NewToolResultError("trace_files is required"), nil
		}
		metrics := req.GetStringSlice("metrics", []string{"tool_trajectory_avg_score"})
		traceFormat := req.GetString("trace_format", "")
		evalSetFile := req.GetString("eval_set_file", "")
		judgeModelName := req.GetString("judge_model", judge.DefaultModel)
		threshold := req.GetFloat("threshold", 0.5)
		rubricTexts := req.GetStringSlice("rubrics", nil)
		jevQuestionsFile := req.GetString("jev_questions_file", "")

		var err error
		for i, p := range traceFiles {
			if traceFiles[i], err = confineToRoot(root, p); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}
		for _, p := range []*string{&evalSetFile, &jevQuestionsFile} {
			if *p == "" {
				continue
			}
			if *p, err = confineToRoot(root, *p); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}

		result, err := runEvaluateTraces(ctx, traceFiles, metrics, traceFormat, evalSetFile, judgeModelName, threshold, rubricTexts, jevQuestionsFile)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultStructuredOnly(result), nil
	}
}

// runEvaluateTraces is evaluate_traces' core logic, deliberately mirroring runCmd's loop (cmd/ego/main.go)
// rather than sharing code with it - same reason runCmd's own runJudgeMetric/runVertexEvalMetric already mirror
// internal/api/evaluate.go's instead of importing internal/api: this stays independent of any HTTP-specific
// machinery. Unlike runCmd (which only reads Jaeger JSON, matching the historical CLI quickstart exactly), this
// uses internal/loader's format auto-detection + adk.ConvertTrace.
func runEvaluateTraces(
	ctx context.Context,
	traceFiles, metrics []string,
	traceFormat, evalSetFile, judgeModelName string,
	threshold float64,
	rubricTexts []string,
	jevQuestionsFile string,
) (*evaluateTracesResultMCP, error) {
	var evalSet *adk.EvalSet
	if evalSetFile != "" {
		var err error
		evalSet, err = eval.LoadEvalSet(evalSetFile)
		if err != nil {
			return nil, fmt.Errorf("loading eval set: %w", err)
		}
	}

	thresholds := make(map[string]float64, len(metrics))
	for _, m := range metrics {
		thresholds[m] = threshold
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
		client, err := judge.NewGenAIModel(ctx, "", judgeModelName)
		if err != nil {
			return nil, fmt.Errorf("setting up judge model: %w", err)
		}
		judgeModelClient = client
	}

	var vertexEvalClient *vertexeval.Client
	if len(requestedVertexMetrics) > 0 {
		client, err := vertexeval.NewClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("setting up Vertex AI Managed Eval Service client: %w", err)
		}
		vertexEvalClient = client
	}

	rubricObjects := judge.RubricsFromStrings(rubricTexts)

	var jevRequests []decision.Request
	var jevClient *decision.Client
	if len(requestedJevMetrics) > 0 || jevQuestionsFile != "" {
		var custom []byte
		if jevQuestionsFile != "" {
			var err error
			if custom, err = os.ReadFile(jevQuestionsFile); err != nil {
				return nil, fmt.Errorf("reading jev questions: %w", err)
			}
		}
		var err error
		if jevRequests, err = decision.Requests(requestedJevMetrics, custom, threshold); err != nil {
			return nil, err
		}
		if jevClient, err = decision.NewClient("", ""); err != nil {
			return nil, err
		}
	}

	result := &evaluateTracesResultMCP{Passed: true}
	for _, path := range traceFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("reading %s: %v", path, err))
			continue
		}
		traces, err := loader.ParseFile(path, content, traceFormat)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("loading %s: %v", path, err))
			continue
		}

		for _, tr := range traces {
			conv := adk.ConvertTrace(tr)
			expected := eval.FindExpectedInvocations(conv.Invocations, evalSet)

			var results []eval.Result
			if len(localMetrics) > 0 {
				results = append(results, eval.RunMetrics(conv.Invocations, evalSet, localMetrics, eval.MatchExact, thresholds)...)
			}
			for _, m := range requestedJudgeMetrics {
				r, err := runJudgeMetric(ctx, m, judgeModelClient, conv.Invocations, expected, rubricObjects, judge.DefaultNumSamples, thresholds[m])
				if err != nil {
					r = eval.Result{MetricName: m, Error: err.Error()}
				}
				results = append(results, r)
			}
			for _, m := range requestedVertexMetrics {
				r, err := runVertexEvalMetric(ctx, m, vertexEvalClient, conv.Invocations, expected, thresholds[m])
				if err != nil {
					r = eval.Result{MetricName: m, Error: err.Error()}
				}
				results = append(results, r)
			}
			if len(jevRequests) > 0 {
				jevResults, err := decision.Evaluate(ctx, jevClient, conv.Invocations, expected, jevRequests)
				if err != nil {
					for _, r := range jevRequests {
						jevResults = append(jevResults, eval.Result{MetricName: r.Metric.Name, Error: err.Error()})
					}
				}
				results = append(results, jevResults...)
			}

			te := traceEvalMCP{TraceID: tr.TraceID, NumInvocations: len(conv.Invocations), Warnings: conv.Warnings}
			for _, r := range results {
				// An errored metric gets status "ERROR" (as internal/api/evaluate.go's toMetricResultDTO does for the
				// REST API/UI) and fails the top-level passed field, so a crashed metric can't silently read as passing.
				status := string(r.Status)
				if r.Error != "" {
					status = "ERROR"
				}
				ms := metricScoreMCP{Metric: r.MetricName, Status: status, Error: r.Error}
				if r.Error == "" {
					score := r.Score
					ms.Score = &score
				}
				if status == "FAILED" || status == "ERROR" {
					result.Passed = false
				}
				te.Metrics = append(te.Metrics, ms)
			}
			result.Traces = append(result.Traces, te)
		}
	}
	return result, nil
}

// confineToRoot resolves p (symlinks included) and returns it only if it lies inside root, so an MCP client
// cannot make evaluate_traces read, and send to a judge model, files elsewhere on the machine.
func confineToRoot(root, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %v", p, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the allowed directory %s (set --trace-dir to change it)", p, root)
	}
	return resolved, nil
}

// tokenSafeFor reports whether a credential may be sent to serverURL: https, or plain http to this machine.
func tokenSafeFor(serverURL string) bool {
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
