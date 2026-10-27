// Package decision implements the jev_* eval family: instead of prompting
// a text judge and parsing its reply, it sends each invocation to a decision
// model as a structured state plus typed questions and scores the typed
// answers directly. Any server that speaks that API works (Jev through the
// default endpoint, or a model served by Ollama at /v1/systemone); the
// endpoint, model and key are configuration. The ADK metrics in
// internal/judge stay Gemini-only.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

// Client calls the Decisions API.
type Client struct {
	httpClient *http.Client
	url        string
	apiKey     string
	model      string
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
	return setting("EGO_DECISION_API_KEY", "JEV_API_KEY") != "" || setting("EGO_DECISION_URL", "JEV_API_URL") != ""
}

// NewClient creates a client. apiKey falls back to EGO_DECISION_API_KEY then JEV_API_KEY, model to
// EGO_DECISION_MODEL, JEV_MODEL, then DefaultModel; EGO_DECISION_URL (JEV_API_URL) overrides the endpoint, and a
// state larger than EGO_DECISION_MAX_STATE_BYTES is shortened before it is sent (for a model with a small context).
// The default endpoint needs a key; another one gets the key only if there is one.
func NewClient(apiKey, model string) (*Client, error) {
	if apiKey == "" {
		apiKey = setting("EGO_DECISION_API_KEY", "JEV_API_KEY")
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
	if apiKey == "" && url == DefaultURL {
		return nil, fmt.Errorf("jev_* metrics need an API key for the default endpoint: set EGO_DECISION_API_KEY (or JEV_API_KEY, or pass --jev-api-key), or point EGO_DECISION_URL at your own decision model")
	}
	maxState := 0
	if v := setting("EGO_DECISION_MAX_STATE_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("EGO_DECISION_MAX_STATE_BYTES must be a number of bytes, got %q", v)
		}
		maxState = n
	}
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		url:        url,
		apiKey:     apiKey,
		model:      model,
		maxState:   maxState,
		backoff:    time.Second,
	}, nil
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
	body, err := json.Marshal(map[string]any{
		"model":     c.wireModel(),
		"state":     state,
		"questions": questions,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding decision request: %w", err)
	}

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

func (c *Client) decideOnce(ctx context.Context, body []byte) (answers map[string]Answer, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
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
