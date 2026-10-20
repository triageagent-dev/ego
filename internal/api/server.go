package api

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/online"
	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
	uiassets "github.com/triageagent-dev/ego/ui"
)

// Version defaults to "dev" for a plain `go build`/`go run`; the release
// build (see .ko.yaml) overrides it via -ldflags -X at link time to match
// the image tag, so GET /api/health reports the version actually running
// instead of a stale placeholder.
var Version = "dev"

const persistInterval = 10 * time.Second

// Neither OTLP receiver is ever gated by sessionSecret: trace ingestion is a service-to-service push, not a
// UI/API call a signed session cookie applies to. Health lives on its own port rather than being one exempted
// path on addr's mux: mixing an unauthenticated route into an otherwise-gated mux is exactly the shape of bug
// that took this service down in the cluster (Kubernetes probes hit /api/health with no credentials; when it
// shared addr and only that one path was carved out of the auth wrapper, a routing slip there would have
// silently exposed - or, as it briefly did before the carve-out, silently broken - the probe). A separate
// listener makes "health is never gated" true by construction instead of by a mux-ordering detail.
//
// sessionSecret gates every other route behind the same signed
// ego_session cookie readSessionCookie validates. If githubOAuth is
// non-nil, this service also runs its own /auth/login -> /auth/callback ->
// /auth/logout GitHub OAuth flow (oauth.go) to mint that cookie itself -
// it depends on no other process being up to log a user in. If
// githubOAuth is nil but sessionSecret is set, this service only
// validates cookies minted elsewhere sharing that secret (e.g. for a
// transitional period running alongside another service that mints them);
// if both are empty, auth is disabled entirely (see requireSession).
//
// The UI is baked into the binary via the ui package's go:embed (see Makefile's build-ui - embedding reads
// whatever was on disk in ui/dist at `go build` time). Blocks until any listener returns an error or the
// process receives SIGINT/SIGTERM, in which case it flushes a final snapshot before returning.
func Serve(addr, otlpAddr, otlpGRPCAddr, healthAddr, sessionDBPath, sessionSecret string, githubOAuth *GitHubOAuthConfig, adminGitHubUsers []string, defaultRole string, limits SessionLimits, onlineConfigPath string) error {
	hub := newSSEHub()

	var store *SessionStore
	var archive *SQLiteStore
	if sessionDBPath != "" {
		var err error
		archive, err = NewSQLiteStore(sessionDBPath)
		if err != nil {
			return fmt.Errorf("opening session archive: %w", err)
		}
		store = NewSessionStoreWithArchive(hub, archive, limits)
		if err := store.LoadPersisted(); err != nil {
			return err
		}
		stop := make(chan struct{})
		defer close(stop)
		store.StartPersistence(persistInterval, stop)
		defer store.Close()
	} else {
		store = NewSessionStore(hub)
	}

	// Online evaluation (docs/online-evaluation.md) needs the archive: it
	// stores scores and alerts there and reads spans back by session.
	var onlineEngine *OnlineEngine
	if onlineConfigPath != "" {
		if archive == nil {
			return fmt.Errorf("--online-config needs --session-db")
		}
		cfg, err := online.Load(onlineConfigPath)
		if err != nil {
			return err
		}
		onlineEngine = NewOnlineEngine(cfg, archive, store, limits.UsageTenantAttr)
		store.SetOnComplete(onlineEngine.Enqueue)
		onlineStop := make(chan struct{})
		defer close(onlineStop)
		onlineEngine.Start(onlineStop)
	}

	var roleStore *RoleStore
	if sessionSecret != "" {
		role, err := ParseRole(defaultRole)
		if err != nil {
			return fmt.Errorf("default role: %w", err)
		}
		roleStore, err = NewRoleStore(archive, role, adminGitHubUsers)
		if err != nil {
			return fmt.Errorf("opening role store: %w", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/streaming/sessions", sessionsHandler(store))
	mux.HandleFunc("/api/streaming/get-trace", getTraceHandler(store))
	mux.HandleFunc("/api/streaming/session-spans", sessionSpansHandler(store))
	mux.HandleFunc("/api/streaming/create-eval-set", createEvalSetHandler(store))
	mux.HandleFunc("/stream/ui-updates", uiUpdatesHandler(hub))
	mux.HandleFunc("/ws/ui-updates", wsUpdatesHandler(hub))
	mux.HandleFunc("/api/config", configHandler)
	mux.HandleFunc("/api/metrics", metricsHandler)
	mux.HandleFunc("/api/usage", usageHandler(archive))
	mux.HandleFunc("/api/usage/calls", usageCallsHandler(archive))
	mux.HandleFunc("/api/runs", listRunsHandler(archive))
	mux.HandleFunc("/api/runs/{id}", getRunHandler(archive))
	mux.HandleFunc("/api/runs/{id}/results", getRunResultsHandler(archive))
	mux.HandleFunc("/api/evalsets", evalSetsHandler(archive))
	mux.HandleFunc("/api/evalsets/{id}", getEvalSetHandler(archive))
	mux.HandleFunc("/api/convert", convertHandler)
	mux.HandleFunc("/api/evaluate", evaluateHandler(archive))
	mux.HandleFunc("/api/evaluate/stream", evaluateStreamHandler(archive))
	mux.HandleFunc("/api/online/status", onlineStatusHandler(onlineEngine))
	mux.HandleFunc("/api/online/series", onlineSeriesHandler(onlineEngine))
	mux.HandleFunc("/api/online/alerts", onlineAlertsHandler(onlineEngine))
	mux.HandleFunc("/api/admin/roles", adminRolesHandler(roleStore))
	mux.HandleFunc("/api/admin/roles/{id}", adminRoleByIDHandler(roleStore))

	uiDist, err := fs.Sub(uiassets.DistFS, "dist")
	if err != nil {
		return fmt.Errorf("opening embedded UI assets: %w", err)
	}
	if _, err := fs.Stat(uiDist, "index.html"); err == nil {
		mux.Handle("/", http.FileServer(http.FS(uiDist)))
		log.Print("serving UI embedded in binary")
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not built: run `make build-ui` before `go build` (ui/dist/index.html not found in embedded assets)", http.StatusNotFound)
		})
	}

	otlpMux := http.NewServeMux()
	otlpMux.HandleFunc("/v1/traces", otlpTracesHandler(store))

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/api/health", healthHandler)

	if sessionSecret != "" {
		log.Printf("gh-auth enabled for %s: requires a signed ego_session cookie", addr)
	}

	errCh := make(chan error, 4)
	go func() {
		log.Printf("ego health check listening on %s (GET /api/health, never gated)", healthAddr)
		errCh <- http.ListenAndServe(healthAddr, healthMux)
	}()
	go func() {
		log.Printf("ego OTLP/HTTP receiver listening on %s (POST /v1/traces)", otlpAddr)
		errCh <- http.ListenAndServe(otlpAddr, otlpMux)
	}()
	go func() {
		lis, err := net.Listen("tcp", otlpGRPCAddr)
		if err != nil {
			errCh <- fmt.Errorf("listening on %s for OTLP/gRPC: %w", otlpGRPCAddr, err)
			return
		}
		log.Printf("ego OTLP/gRPC receiver listening on %s", otlpGRPCAddr)
		errCh <- newOTLPGRPCServer(store).Serve(lis)
	}()
	// /api/health and /auth/me stay reachable on addr itself, unwrapped by
	// requireSession, alongside the dedicated health port: k8s probes hit
	// healthAddr, but the UI's own client code also calls
	// config.api.endpoints.health (a same-origin connectivity check) and
	// auth/me (its "am I logged in" check, which must answer with 401 JSON
	// rather than a redirect even when there's no session - see
	// authMeHandler). /auth/login, /auth/callback and /auth/logout must
	// likewise stay unwrapped: a not-yet-authenticated user has to be able
	// to reach them to *become* authenticated in the first place - wrapping
	// them in requireSession would be a login flow that redirects anyone
	// without a session straight back to itself. Everything else on addr
	// goes through requireSession.
	topMux := http.NewServeMux()
	ghTokens := newGitHubTokenValidator(githubOAuth)
	roleOrg := ""
	if githubOAuth != nil {
		roleOrg = githubOAuth.Org
	}
	topMux.HandleFunc("/api/health", healthHandler)
	topMux.HandleFunc("/auth/me", authMeHandler(sessionSecret, ghTokens, roleStore, roleOrg))
	if githubOAuth != nil {
		topMux.HandleFunc("/auth/login", authLoginHandler(githubOAuth))
		topMux.HandleFunc("/auth/callback", authCallbackHandler(githubOAuth, roleStore))
		topMux.HandleFunc("/auth/logout", authLogoutHandler())
		log.Printf("independent GitHub OAuth enabled for %s: org=%q, callback=%q", addr, githubOAuth.Org, githubOAuth.callbackURL())
	}
	if roleStore != nil {
		log.Printf("multi-user RBAC enabled for %s: default role=%q, %d role binding(s) loaded", addr, defaultRole, len(roleStore.List()))
	}
	topMux.Handle("/", requireSession(sessionSecret, ghTokens, withRole(roleStore, roleOrg, sessionSecret, ghTokens, mux.ServeHTTP)))

	go func() {
		log.Printf("ego listening on %s", addr)
		errCh <- http.ListenAndServe(addr, topMux)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		// The deferred store.Close()/close(stop) above still run on this
		// return, flushing a final snapshot before the process exits.
		log.Printf("received %s, shutting down", sig)
		return nil
	}
}

// healthHandler implements GET /api/health, enveloped the same way..., "error": null} response shape (confirmed
// by a live side-by-side call, not assumed).
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]string{
			"status":  "ok",
			"version": Version,
		},
		"error": nil,
	})
}

// otlpTracesHandler implements POST /v1/traces (ExportTraceServiceRequest), accepting both
// application/x-protobuf and OTLP/JSON.
func otlpTracesHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		reqBody, status, err := otlpRequestBody(r)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		defer reqBody.Close()

		contentType := r.Header.Get("Content-Type")
		var body map[string]any

		if strings.Contains(contentType, "application/x-protobuf") {
			raw, err := io.ReadAll(reqBody)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			body, err = otlp.DecodeProtobufTraces(raw)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			if err := json.NewDecoder(reqBody).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		store.Ingest(body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"partialSuccess":{}}`))
	}
}

// otlpRequestBody returns r's body with its Content-Encoding undone. The returned status is only meaningful
// when err is non-nil.
func otlpRequestBody(r *http.Request) (io.ReadCloser, int, error) {
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
		return r.Body, 0, nil
	case "gzip":
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid gzip body: %w", err)
		}
		return zr, 0, nil
	default:
		return nil, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
}

func sessionsHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": store.List(), "error": nil})
	}
}

// createEvalSetRequest is POST /api/streaming/create-eval-set's request body.
type createEvalSetRequest struct {
	SessionID string `json:"session_id"`
	EvalSetID string `json:"eval_set_id"`
}

// createEvalSetHandler implements POST /api/streaming/create-eval-set: batch-converts a session's full trace
// into an ADK EvalSet golden reference file.
func createEvalSetHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req createEvalSetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		tr, ok := store.Trace(req.SessionID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		result := adk.ConvertTrace(tr)
		if len(result.Invocations) == 0 {
			detail := "Failed to convert trace"
			if len(result.Warnings) > 0 {
				detail = strings.Join(result.Warnings, "; ")
			}
			http.Error(w, detail, http.StatusBadRequest)
			return
		}

		evalSet := adk.EvalSet{
			EvalSetID: req.EvalSetID,
			EvalCases: []adk.EvalCase{{
				EvalID:       "case_1",
				Conversation: result.Invocations,
			}},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"evalSet":        evalSet,
				"numInvocations": len(result.Invocations),
			},
			"error": nil,
		})
	}
}

// getTraceRequest is POST /api/streaming/get-trace's request body.
type getTraceRequest struct {
	SessionID string `json:"session_id"`
}

// getTraceHandler implements POST /api/streaming/get-trace: hands back a session's full trace as OTLP JSONL
// text, the same format /api/streaming/create-eval-set and /api/evaluate consume, so the UI can blob it
// straight into a re-uploaded .jsonl file (LiveStreamingView.tsx's handleContinueToEvaluation,
// AnnotationQueueView.tsx).
func getTraceHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req getTraceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		tr, ok := store.Trace(req.SessionID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		content, err := otlp.EncodeTraceJSONL(tr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"sessionId":    req.SessionID,
				"traceContent": string(content),
				"numSpans":     len(tr.AllSpans),
			},
			"error": nil,
		})
	}
}

// sessionSpanDTO is one row of GET /api/streaming/session-spans: a span with every attribute (status and
// exception fields included, see otlp.parseSpan) and its depth in the trace tree, so the UI can draw an
// indented list without rebuilding the tree.
type sessionSpanDTO struct {
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	OperationName string         `json:"operation_name"`
	StartTime     int64          `json:"start_time"`
	Duration      int64          `json:"duration"`
	Depth         int            `json:"depth"`
	Tags          map[string]any `json:"tags"`
}

// sessionSpansHandler implements GET /api/streaming/session-spans?session_id=: the session's spans in
// depth-first order (children by start time), each with its attributes.
func sessionSpansHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("session_id")
		out, ok := store.SpanRows(id)
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "session not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"sessionId": id, "spans": out}, "error": nil})
	}
}

// spanDTO is the JSON-serializable form of internal/trace.Span. Exposed
// shape is provisional - matching the UI's exact undocumented response
// contract is follow-up work; see README.md.
type spanDTO struct {
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	OperationName string         `json:"operation_name"`
	StartTime     int64          `json:"start_time"`
	Duration      int64          `json:"duration"`
	Tags          map[string]any `json:"tags"`
	Children      []spanDTO      `json:"children,omitempty"`
}

func spanToDTO(s *tracepkg.Span) spanDTO {
	children := make([]spanDTO, 0, len(s.Children))
	for _, c := range s.Children {
		children = append(children, spanToDTO(c))
	}
	return spanDTO{
		SpanID:        s.SpanID,
		ParentSpanID:  s.ParentSpanID,
		OperationName: s.OperationName,
		StartTime:     s.StartTime,
		Duration:      s.Duration,
		Tags:          s.Tags,
		Children:      children,
	}
}
