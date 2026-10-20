package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestConfigHandler(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "some-key")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	configHandler(rec, req)

	var body struct {
		Data struct {
			APIKeys struct {
				Google    bool `json:"google"`
				Anthropic bool `json:"anthropic"`
				OpenAI    bool `json:"openai"`
			} `json:"apiKeys"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.Data.APIKeys.Google {
		t.Error("apiKeys.google = false, want true (GEMINI_API_KEY was set)")
	}
	if body.Data.APIKeys.Anthropic || body.Data.APIKeys.OpenAI {
		t.Error("apiKeys.anthropic/openai = true, want false (unset)")
	}
	if body.Error != nil {
		t.Errorf("error = %v, want nil", body.Error)
	}
}

// TestConfigHandler_VertexAINoAPIKey replicates the "SA/Vertex AI auth, no
// API keys set anywhere" deployment: the UI must not red-flag google as
// unconfigured just because no literal GOOGLE_API_KEY/GEMINI_API_KEY is
// present, since judge.NewGenAIModel works fine via ADC in that mode.
func TestConfigHandler_VertexAINoAPIKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "some-project")

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	configHandler(rec, req)

	var body struct {
		Data struct {
			APIKeys struct {
				Google bool `json:"google"`
			} `json:"apiKeys"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.Data.APIKeys.Google {
		t.Error("apiKeys.google = false, want true (Vertex AI configured via GOOGLE_GENAI_USE_VERTEXAI+GOOGLE_CLOUD_PROJECT, no API key needed)")
	}
}

// TestConfigHandler_VertexAIFlagWithoutProject checks the flag alone,
// without a project to call against, still red-flags - GOOGLE_CLOUD_PROJECT
// is required for the SDK to reach Vertex AI at all.
func TestConfigHandler_VertexAIFlagWithoutProject(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	configHandler(rec, req)

	var body struct {
		Data struct {
			APIKeys struct {
				Google bool `json:"google"`
			} `json:"apiKeys"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Data.APIKeys.Google {
		t.Error("apiKeys.google = true, want false (no GOOGLE_CLOUD_PROJECT set)")
	}
}

func TestMetricsHandler_ReportsWorkingAccurately(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	rec := httptest.NewRecorder()
	metricsHandler(rec, req)

	var body struct {
		Data []metricInfo `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Data) != len(allMetrics) {
		t.Fatalf("got %d metrics, want %d", len(body.Data), len(allMetrics))
	}

	seen := map[string]bool{}
	for _, m := range body.Data {
		seen[m.Name] = m.Working
	}
	for name, wantWorking := range workingMetrics {
		if seen[name] != wantWorking {
			t.Errorf("metric %q working = %v, want %v", name, seen[name], wantWorking)
		}
	}
	// A known-unimplemented metric must be reported as such, not silently
	// claimed as working.
	if seen["response_evaluation_score"] {
		t.Error("response_evaluation_score reported as working, but it's genuinely out of scope (see docs/STATUS.md)")
	}
}

func TestRunsHandler_NoStoreReturns503(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	rec := httptest.NewRecorder()
	listRunsHandler(nil)(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (matches ui/src/api/client.ts's StorageUnavailableError check)", rec.Code)
	}
}

func TestRunsHandler_WithStoreListsRuns(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	rec := httptest.NewRecorder()
	listRunsHandler(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data  []runDTO `json:"data"`
		Error any      `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Data == nil || len(body.Data) != 0 {
		t.Errorf("data = %v, want an empty (non-nil) list when no runs have been recorded yet", body.Data)
	}
}

func TestAuthMeHandler_NoSession(t *testing.T) {
	handler := authMeHandler("some-secret", nil, nil, nil, "")
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	var body struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Authenticated {
		t.Error("authenticated = true, want false")
	}
}

func TestAuthMeHandler_ValidSession(t *testing.T) {
	const secret = "auth-me-secret"
	handler := authMeHandler(secret, nil, nil, nil, "")
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mintTestCookie(t, "alice", secret)})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Authenticated bool   `json:"authenticated"`
		Username      string `json:"username"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.Authenticated || body.Username != "alice" {
		t.Errorf("body = %+v, want authenticated=true username=alice", body)
	}
}

// TestUnauthenticatedRoutesNeverBlockOnMissingSession replicates the
// regression this fixes: /api/health and /auth/me must stay reachable on
// the same port as the gated routes (the UI's own client code calls them
// at that base URL), without requiring a valid session - verified by
// building the exact topMux composition Serve uses.
func TestUnauthenticatedRoutesNeverBlockOnMissingSession(t *testing.T) {
	topMux := http.NewServeMux()
	topMux.HandleFunc("/api/health", healthHandler)
	topMux.HandleFunc("/auth/me", authMeHandler("secret", nil, nil, nil, ""))
	gatedMux := http.NewServeMux()
	gatedMux.HandleFunc("/api/streaming/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	topMux.Handle("/", requireSession("secret", nil, nil, gatedMux))

	for _, path := range []string{"/api/health", "/auth/me"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		topMux.ServeHTTP(rec, req)
		if rec.Code == http.StatusFound {
			t.Errorf("%s redirected (302) with no session - must answer directly, not be caught by the auth gate", path)
		}
	}

	// Sanity: the gated route still requires a session.
	req := httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)
	rec := httptest.NewRecorder()
	topMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("gated API route status = %d, want 401 with no session", rec.Code)
	}
}

// With auth off there is no session to expire: /auth/me answers 200 so the
// UI shows no "session expired" banner.
func TestAuthMeHandler_AuthOff(t *testing.T) {
	rec := httptest.NewRecorder()
	authMeHandler("", nil, nil, nil, "")(rec, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"authEnabled":false`)) {
		t.Errorf("code = %d body = %s", rec.Code, rec.Body.String())
	}
}
