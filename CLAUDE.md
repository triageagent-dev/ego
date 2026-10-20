# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

ego is reality testing for AI agents: it scores their behavior from OpenTelemetry traces, without re-executing them. **`docs/STATUS.md` is the source of truth** for what is supported, known gaps, and what is out of scope. Check it before assuming a feature exists, and update it when you add something or find a gap.

## Commands

```bash
cd ui && npm ci && npm run build      # or: make build-ui — MUST run before any Go build/vet/test (see below)
go build -o bin/ego ./cmd/ego   # or: make build
go test ./...                                  # or: make test
go test ./internal/eval/... -run TestRouge1FMeasure -v   # single test
go vet ./...
gofmt -l .                                     # must print nothing (CI fails otherwise); `make fmt` only lists
cd ui && npx tsc -b                      # UI type check
```

`ui/embed.go` does `//go:embed all:dist`, so `ui/dist` must exist before compiling anything that imports the
`ui` package; the binary embeds whatever is on disk in `ui/dist` at build time.

Run things:
- `make run ARGS="samples/helm.json --eval-set samples/eval_set_helm.json -m tool_trajectory_avg_score"` -
  should print `[PASS] ... score=1.00`.
- `make serve` - REST API + UI on `:8001`, OTLP/HTTP on `:4318`, OTLP/gRPC on `:4317`, health on `:9090`.
- `make dev-backend` + `make dev-frontend` - Vite dev server on `:5173` calling backend at `:8001` directly.
- `./bin/ego mcp` - MCP server on stdio, talks to a running `serve` (`EGO_SERVER_URL`).

CI (`.github/workflows/ci.yml`): UI build → `go build`/`vet`/`gofmt -l`/`test`; separate UI lint (non-blocking)
+ build; Docker image build.

## Architecture

Pipeline: **trace loading → conversion to ADK Invocations → metric evaluation**, plus a **live ingestion →
streaming** path for the UI.

- `cmd/ego/`: subcommands `run`, `serve`, `auth mint-token`, `mcp`. `main.go` holds metric
  dispatch: names in `judgeMetrics` go to `internal/judge` (Gemini via `google.golang.org/genai`, local prompt
  built here); names in `vertexEvalMetrics` go to `internal/vertexeval` (Vertex AI Managed Eval Service via
  ADC, structured instances, scored server-side); everything else goes to `eval.RunMetrics` (local, no
  API key/context dependency by design). `mcp.go` is a thin MCP client over the `serve` REST API.
- `internal/trace` (Span/Trace model, Jaeger JSON) → `internal/loader` (format auto-detect: Jaeger vs OTLP
  JSON/JSONL) → `internal/adk` (`ConvertTrace` auto-detects ADK `gcp.vertex.agent.*` attrs vs
  GenAI-semconv/OpenInference, `genai_converter.go`) → `internal/eval` / `internal/judge` / `internal/vertexeval`.
- `internal/otlp`: AnyValue decoding, protobuf↔JSON bridge, OTLP export → Span/Trace.
- `internal/incremental`: real-time span/log → conversation-element updates pushed to the UI.
- `internal/api`: everything behind `serve`.
  - OTLP/HTTP and OTLP/gRPC receivers feed the **same** `SessionStore` and conversion path - don't
    special-case one. Sessions group by `ego.session_name` → `gen_ai.conversation.id` → synthetic
    fallback; on completion `sessions.go`'s `completeSession` converts to invocations.
  - Live UI feed is WebSocket `GET /ws/ui-updates` (`wsupdates.go`), because gateways buffer long-lived SSE.
    `GET /stream/ui-updates` (`sse.go`, same hub) remains as a curl-able fallback. Both differ from `/ws/traces`
    (an SDK ingestion channel ego does not implement).
  - Persistence is opt-in (`--session-db` / `EGO_SESSION_DB_PATH`): one `SQLiteStore` holds sessions
    (whole-session JSON blob per row - adding a `Session` field needs no migration), run history
    (`runsstore.go`), eval sets (`evalsetsstore.go`), and role bindings (`rolesstore.go`). Only the newest
    `--session-memory` completed sessions (default 200) live in memory; older ones are read back by ID on demand
    (`lookupLocked`). `--session-retention` (default 30 days) deletes stale sessions daily, then VACUUMs.
  - Auth (`oauth.go`, `sessionauth.go`, `githubtoken.go`) is active only with `--session-secret` /
    `EGO_SESSION_SECRET`: GitHub OAuth signed cookie, or bearer tokens (minted via `auth mint-token`,
    or a raw GitHub token validated against org membership). `server.go` wraps the main mux in
    `requireSession` + `withRole`; `/api/health` is never gated; OTLP receivers use the optional ingest token (`ingestauth.go`), not the session.
  - RBAC (`roles.go`, `roleshandlers.go`): `RoleStore` exists only when a session
    secret is set (otherwise `nil` and the feature is fully inert - preserve that). Roles admin > member >
    viewer; viewers get 403 on write endpoints. An agent-scoped role (`RoleInfo.Scoped`) fails closed:
    sessions, traces, runs and the hub feed (`registerFor`) are filtered by agent, views that cannot be attributed
    to an agent use `denyScoped`, and unknown teams give `Unresolved` (refused, never `defaultRole`). Tenant limits (`RoleInfo.Tenants`: keys, `<sender>/*`, `*`) work the same way; ingest resolves a token to a sender (`SenderStore`) and the tenant key is `<sender>/<label>`.
- `ui/`: React + Vite + Ant Design, embedded into the binary. Edited here like any other code.
- `samples/`: sample traces and eval sets; used by tests and the quickstart.

## Conventions

- Comments say why, not where the code came from. Keep names and comments about this codebase only.
- Metrics that are not supported belong in `eval.NotYetImplemented` so they fail with a clear error rather than
  scoring 0. New judge metrics: add to `judgeMetrics` + `runJudgeMetric` in `main.go`.
- LLM calls go through the `judge.Model` interface (`internal/judge/client.go`) so parsing/aggregation is
  testable with a scripted fake and no API key.
- No CGO (`modernc.org/sqlite` is chosen for that; image is `cgr.dev/chainguard/static`). One static binary, no
  other runtime.
- Deploy is Helm + ko: `make image` (clean tree; builds `ui/dist`, ko-pushes `<BASE_VERSION>.<count>-<sha>`),
  `make deploy-diff`, `make deploy`. Chart in `charts/ego`; real values in `deploy/values.yaml`
  (gitignored), sanitized template `deploy/values.example.yaml`. A new root-level `/api/...` endpoint needs an
  entry in `httpRoute.rootPaths` or the gateway answers 404.
