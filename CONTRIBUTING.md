# Contributing to ego

Thanks for your interest. Read [docs/STATUS.md](docs/STATUS.md) first: it lists what is supported, what is
missing and what is out of scope, so effort doesn't collide with something already tracked.

**Status: work in progress.** Expect breaking changes between releases.

## Ground rules

- **One static binary.** No CGO (`modernc.org/sqlite`), no other runtime. Judge-model calls go through
  [`google.golang.org/genai`](https://pkg.go.dev/google.golang.org/genai); Vertex AI Managed Eval calls go
  through `internal/vertexeval`.
- **Test what you change.** `judge.Model` and similar interfaces exist so parsing and aggregation can be tested
  against a scripted fake without an API key. Add a test with any new metric, handler or non-trivial fix.
- **Small, focused PRs.** One metric, endpoint or fix per PR.
- **Comments say why.** Don't narrate where code came from.

## Development setup

```bash
make build-ui build         # the UI is embedded, so build it first
make test vet
gofmt -l .                  # must print nothing

cd ui && npm ci && npx tsc --noEmit
```

`make serve` runs the full server (REST API, OTLP/HTTP and gRPC receivers, embedded UI) on `:8001`, `:4318` and
`:4317`. See the [README](README.md) for CLI usage and settings.

## Before opening a PR

- [ ] `go build ./...`, `go vet ./...` and `gofmt -l .` are clean.
- [ ] `go test ./...` passes (call out any unrelated failure).
- [ ] If you touched `ui/`: `npm run build` is clean.
- [ ] New or changed behavior has a test.
- [ ] `docs/STATUS.md` is updated if you added something listed as unsupported, or found a gap.

## Reporting issues

Open an issue with what you ran (CLI command or API request), what you expected, what happened, and, for a
scoring discrepancy, the trace and eval-set file (redacted) that reproduces it.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
