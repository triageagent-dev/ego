// Package decision implements the jev_* eval family: instead of prompting
// a text judge and parsing its reply, it sends each invocation to a decision
// model as a structured state plus typed questions and scores the typed
// answers directly. Any server that speaks that API works (Jev through the
// default endpoint, or a model served by Ollama at /v1/systemone), and so does
// Google's Gemini Decisions API, whose request differs a little and which is
// known by its host (or by EGO_DECISION_PROTOCOL=gemini behind a gateway); the endpoint, model and key are configuration. The ADK
// metrics in internal/judge stay Gemini-only.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/redact"
)

// DefaultModel is the model the jev_* metrics use unless EGO_DECISION_MODEL
// (or JEV_MODEL) or a caller overrides it.
const DefaultModel = "jev-1.13"

// apiNamespace is the provider namespace the default endpoint expects in
// front of a bare model name ("jev-1.13" is sent as "<namespace>jev-1.13").
// A name that already carries a namespace is sent as is, and so is every name
// sent to any other endpoint: a server of your own names its models itself.
const apiNamespace = "typesafe/"

// DefaultURL is the Decisions API endpoint used when no other is configured.
const DefaultURL = "https://openrouter.ai/api/alpha/decisions"

// geminiHost serves Google's Gemini Decisions API, which wants the state as text in `input`, renamed noul criteria
// and the key in x-goog-api-key.
const geminiHost = "generativelanguage.googleapis.com"

// Question is one typed question in a Decisions API request. Type is
// "noul" (a probability that the answer is true, criteria keyed "true" and
// "false") or "choice" (one of the criteria keys).
type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

// Answer is one typed answer. Noul is set for "noul" questions; Choice,
// Probabilities and Confidence for "choice".
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

// Decider answers typed questions about a state object. *Client is the
// real one; tests use a scripted fake.
type Decider interface {
	Decide(ctx context.Context, state map[string]any, questions map[string]Question) (map[string]Answer, error)
}

// podCredentialDir is where Kubernetes mounts the pod's own ServiceAccount token. A token file under it, by name or
// after symlinks, is refused: a projected token with the gateway's audience is the credential meant for the decision
// endpoint, never the token that speaks for the pod to the Kubernetes API.
const podCredentialDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// Client calls the Decisions API.
type Client struct {
	httpClient *http.Client
	url        string
	apiKey     string
	// tokenFile, when set, holds the bearer sent instead of apiKey. It is read on every call: a projected
	// ServiceAccount token is rotated by the kubelet, and a restart to pick the new one up is not wanted.
	tokenFile string
	model     string
	// gemini is set when the request is shaped for the Gemini Decisions API: url is Google's host, or the protocol is
	// set to gemini for a gateway that forwards to it.
	gemini bool
	// googleKey is set when url is Google's own host: the key travels in x-goog-api-key.
	googleKey bool
	// maxState, when above 0, is the size in bytes a state is shortened to before it is sent.
	maxState int
	// backoff is the delay before retry n (doubling); a field so tests
	// don't sleep.
	backoff time.Duration
}

// setting returns the first of the named environment variables that is set. The EGO_DECISION_* names are the
// general ones; the JEV_* names are what deployments of the Jev-only client set and keep working.
func setting(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// Configured reports whether a client can be built from the environment: a key for the default endpoint, or an
// endpoint of your own (which needs no key unless it asks for one).
func Configured() bool {
	return setting("EGO_DECISION_API_KEY", "JEV_API_KEY") != "" || setting("EGO_DECISION_TOKEN_FILE") != "" ||
		setting("EGO_DECISION_URL", "JEV_API_URL") != ""
}

// NewClient creates a client. apiKey falls back to EGO_DECISION_API_KEY then JEV_API_KEY; tokenFile, a file whose
// content is sent as the bearer instead of a key (a projected ServiceAccount token for a gateway that checks a JWT),
// falls back to EGO_DECISION_TOKEN_FILE and is read on every call; model falls back to EGO_DECISION_MODEL, JEV_MODEL,
// then DefaultModel; EGO_DECISION_URL (JEV_API_URL) overrides the endpoint, and a state larger than
// EGO_DECISION_MAX_STATE_BYTES is shortened before it is sent (for a model with a small context). The default endpoint
// needs a key; another one gets the key only if there is one. A key and a token file together are refused, both fill
// the Authorization header. A token file must be an absolute path outside the pod's own ServiceAccount token
// directory, and its token travels only over https, or over http to a loopback host or an in-cluster Service name.
func NewClient(apiKey, tokenFile, model string) (*Client, error) {
	if apiKey == "" {
		apiKey = setting("EGO_DECISION_API_KEY", "JEV_API_KEY")
	}
	if tokenFile == "" {
		tokenFile = setting("EGO_DECISION_TOKEN_FILE")
	}
	if model == "" {
		model = setting("EGO_DECISION_MODEL", "JEV_MODEL")
	}
	if model == "" {
		model = DefaultModel
	}
	url := setting("EGO_DECISION_URL", "JEV_API_URL")
	if url == "" {
		url = DefaultURL
	}
	if apiKey == "" && tokenFile == "" && url == DefaultURL {
		return nil, fmt.Errorf("jev_* metrics need an API key for the default endpoint: set EGO_DECISION_API_KEY (or JEV_API_KEY, or pass --jev-api-key), or point EGO_DECISION_URL at your own decision model")
	}
	protocol := setting("EGO_DECISION_PROTOCOL")
	if protocol != "" && protocol != "gemini" {
		return nil, fmt.Errorf("EGO_DECISION_PROTOCOL must be empty or gemini, got %q", protocol)
	}
	googleKey := geminiURL(url)
	gemini := googleKey || protocol == "gemini"
	if googleKey && (apiKey == "" || tokenFile != "") {
		return nil, errors.New("the Gemini Decisions API (" + geminiHost + ") takes an API key only: set EGO_DECISION_API_KEY (or JEV_API_KEY, or pass --jev-api-key), and no token file")
	}
	if googleKey && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "https://") {
		return nil, errors.New("EGO_DECISION_URL: the Gemini Decisions API key is sent over https only")
	}
	if tokenFile != "" {
		if apiKey != "" {
			return nil, errors.New("EGO_DECISION_TOKEN_FILE cannot be set together with an API key: both fill the Authorization header")
		}
		if err := checkTokenFile(tokenFile); err != nil {
			return nil, err
		}
		if err := checkTokenURL(url); err != nil {
			return nil, err
		}
	}
	maxState := 0
	if v := setting("EGO_DECISION_MAX_STATE_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("EGO_DECISION_MAX_STATE_BYTES must be a number of bytes, got %q", v)
		}
		maxState = n
	}
	httpClient := &http.Client{Timeout: 60 * time.Second}
	if googleKey {
		// Go drops Authorization on a cross-host redirect but forwards x-goog-api-key, so follow none: a 3xx is an error.
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &Client{
		httpClient: httpClient,
		url:        url,
		apiKey:     apiKey,
		tokenFile:  tokenFile,
		model:      model,
		gemini:     gemini,
		googleKey:  googleKey,
		maxState:   maxState,
		backoff:    time.Second,
	}, nil
}

// geminiURL reports whether raw is an endpoint of Google's Gemini Decisions API.
func geminiURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), geminiHost)
}

// checkTokenFile accepts a token file path by its name: absolute, and not the pod's own ServiceAccount token.
// The path after symlinks is checked again on every read, a projected volume is a tree of links.
func checkTokenFile(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("EGO_DECISION_TOKEN_FILE must be an absolute path")
	}
	if under(filepath.Clean(path), podCredentialDir) {
		return errors.New("EGO_DECISION_TOKEN_FILE: the pod's own ServiceAccount token may not be sent to the decision model")
	}
	return nil
}

// checkTokenURL accepts the endpoint a token is sent to when the token cannot leave the cluster or the machine in
// the clear: https to any host, or http to a loopback host or an in-cluster Service name (.svc, .svc.cluster.local).
func checkTokenURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return errors.New("EGO_DECISION_URL must be an absolute URL when a token file is set")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if plainHTTPHost(u.Hostname()) {
			return nil
		}
		return errors.New("EGO_DECISION_URL: a token file travels over plain http only to a loopback host or an in-cluster Service (.svc, .svc.cluster.local); use https")
	}
	return errors.New("EGO_DECISION_URL: the scheme must be https (or http to a loopback host or an in-cluster Service) when a token file is set")
}

// plainHTTPHost reports whether a bearer may travel over http to host.
func plainHTTPHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// under reports whether path is root or below it, by the names alone.
func under(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// bearer returns the credential for one call: the key, or the token file's content read now. The real path of the
// file is checked against the pod's own credentials on every read, so a link planted later cannot redirect it.
func (c *Client) bearer() (string, error) {
	if c.tokenFile == "" {
		return c.apiKey, nil
	}
	real, err := filepath.EvalSymlinks(c.tokenFile)
	if err != nil {
		return "", fmt.Errorf("reading the decision token file: %w", err)
	}
	if under(real, podCredentialDir) {
		return "", errors.New("reading the decision token file: it resolves to the pod's own ServiceAccount token")
	}
	if realCred, err := filepath.EvalSymlinks(podCredentialDir); err == nil && under(real, realCred) {
		return "", errors.New("reading the decision token file: it resolves to the pod's own ServiceAccount token")
	}
	raw, err := os.ReadFile(real)
	if err != nil {
		return "", fmt.Errorf("reading the decision token file: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("reading the decision token file: it is empty")
	}
	return tok, nil
}

// wireModel is the model name as the endpoint is sent it: the default endpoint wants a namespace in front of a
// bare name; any other endpoint is sent the name as configured.
func (c *Client) wireModel() string {
	if c.url == DefaultURL && !strings.Contains(c.model, "/") {
		return apiNamespace + c.model
	}
	return c.model
}

// Model is the model name as callers chose it (without the API namespace).
func (c *Client) Model() string {
	return strings.TrimPrefix(c.model, apiNamespace)
}

// maxAttempts bounds retries on rate limits, server errors and network
// failures.
const maxAttempts = 5

// Decide sends one Decisions API request and returns its answers keyed by
// question name.
func (c *Client) Decide(ctx context.Context, state map[string]any, questions map[string]Question) (map[string]Answer, error) {
	if c.maxState > 0 {
		state = fitState(state, c.maxState)
	}
	request := map[string]any{"model": c.wireModel(), "state": state, "questions": questions}
	if c.gemini {
		// Escaped JSON text hides the key-value pairs from the masking of the whole body, so mask before embedding.
		text, err := json.Marshal(state)
		if err != nil {
			return nil, fmt.Errorf("encoding decision request: %w", err)
		}
		request = map[string]any{"model": c.model, "input": string(redact.Bytes(text)), "questions": geminiQuestions(questions)}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encoding decision request: %w", err)
	}
	body = redact.Bytes(body)

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.backoff << (attempt - 1)):
			}
		}
		answers, retry, err := c.decideOnce(ctx, body)
		if err == nil {
			return answers, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return nil, lastErr
}

// geminiQuestions renames the noul criteria keys Google names differently; other keys pass on as written so
// Google's validation answers for them.
func geminiQuestions(questions map[string]Question) map[string]Question {
	out := make(map[string]Question, len(questions))
	for k, q := range questions {
		if q.Type == "noul" && q.Criteria != nil {
			q.Criteria = maps.Clone(q.Criteria)
			for _, key := range []string{"true", "false"} {
				if v, ok := q.Criteria[key]; ok {
					q.Criteria[key+"_description"] = v
					delete(q.Criteria, key)
				}
			}
		}
		out[k] = q
	}
	return out
}

func (c *Client) decideOnce(ctx context.Context, body []byte) (answers map[string]Answer, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	bearer, err := c.bearer()
	if err != nil {
		return nil, false, err
	}
	switch {
	case c.googleKey:
		req.Header.Set("x-goog-api-key", bearer)
	case bearer != "":
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "ego")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, fmt.Errorf("calling the decision model: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("reading the decision model response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		snippet := raw
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, retry, fmt.Errorf("the decision model returned HTTP %d: %s", resp.StatusCode, snippet)
	}

	var decoded struct {
		Answers map[string]Answer `json:"answers"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false, fmt.Errorf("decoding the decision model response: %w", err)
	}
	return decoded.Answers, false, nil
}
