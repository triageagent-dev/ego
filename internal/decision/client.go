// Package decision implements the jev_* eval family: instead of prompting
// a text judge and parsing its reply, it sends each invocation to the Jev
// decision model as a structured state plus typed questions and scores
// the typed answers directly. The ADK metrics in internal/judge stay Gemini-only.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultModel is the Jev model the jev_* metrics use unless JEV_MODEL or
// a caller overrides it.
const DefaultModel = "jev-1.13"

// apiNamespace is the provider namespace the Decisions API expects in
// front of the model name ("jev-1.13" is sent as "<namespace>jev-1.13").
// A name that already carries it is sent as is.
const apiNamespace = "typesafe/"

// DefaultURL is the Decisions API endpoint.
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
	// backoff is the delay before retry n (doubling); a field so tests
	// don't sleep.
	backoff time.Duration
}

// NewClient creates a client. apiKey falls back to JEV_API_KEY, model to
// JEV_MODEL then DefaultModel; JEV_API_URL overrides the endpoint.
func NewClient(apiKey, model string) (*Client, error) {
	if apiKey == "" {
		apiKey = os.Getenv("JEV_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("jev_* metrics need an API key: set JEV_API_KEY or pass --jev-api-key")
	}
	if model == "" {
		model = os.Getenv("JEV_MODEL")
	}
	if model == "" {
		model = DefaultModel
	}
	url := os.Getenv("JEV_API_URL")
	if url == "" {
		url = DefaultURL
	}
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		url:        url,
		apiKey:     apiKey,
		model:      model,
		backoff:    time.Second,
	}, nil
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
	body, err := json.Marshal(map[string]any{
		"model":     apiNamespace + c.Model(),
		"state":     state,
		"questions": questions,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding Jev request: %w", err)
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
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "ego")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, fmt.Errorf("calling Jev: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("reading Jev response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		snippet := raw
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, retry, fmt.Errorf("Jev returned HTTP %d: %s", resp.StatusCode, snippet)
	}

	var decoded struct {
		Answers map[string]Answer `json:"answers"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false, fmt.Errorf("decoding Jev response: %w", err)
	}
	return decoded.Answers, false, nil
}
