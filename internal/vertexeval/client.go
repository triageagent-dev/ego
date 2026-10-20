// Package vertexeval calls Vertex AI's Managed Evaluation Service (the ":evaluateInstances" GCP REST API),
// which backs google-adk's safety_v1 and multi_turn_*_v1 metrics.
package vertexeval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
)

// cloudPlatformScope is the OAuth scope needed for the aiplatform REST API,
// same Workload Identity / ADC service account already used for the judge
// model's Vertex AI backend (internal/judge.GenAIModel).
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// defaultLocation matches google-adk's own default when
// GOOGLE_CLOUD_LOCATION is unset.
const defaultLocation = "us-central1"

// Client calls the aiplatform ":evaluateInstances" RPC for one project and
// location, authenticated via Application Default Credentials.
type Client struct {
	httpClient *http.Client
	creds      *auth.Credentials
	project    string
	location   string
}

// NewClient builds a Client from GOOGLE_CLOUD_PROJECT/GOOGLE_CLOUD_LOCATION
// (the same env vars internal/judge.GenAIModel's Vertex path reads) and
// Application Default Credentials - a GKE Workload Identity service
// account in production, or gcloud's ADC file locally. No API key is ever
// used or accepted: the Vertex Managed Eval Service has no API-key auth
// mode.
func NewClient(ctx context.Context) (*Client, error) {
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT is not set (required for Vertex AI Managed Eval Service metrics such as safety_v1)")
	}
	location := os.Getenv("GOOGLE_CLOUD_LOCATION")
	if location == "" {
		location = defaultLocation
	}
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{cloudPlatformScope},
	})
	if err != nil {
		return nil, fmt.Errorf("detecting application default credentials: %w", err)
	}
	return &Client{httpClient: http.DefaultClient, creds: creds, project: project, location: location}, nil
}

// evaluateInstancesResponse mirrors EvaluateInstancesResponse from
// google.cloud.aiplatform_v1beta1.types.evaluation_service: a
// protobuf-JSON-transcoded response, so field names are canonical
// lowerCamelCase (verified against google.golang.org/genai's own
// hand-written types for the same aiplatform REST surface, e.g.
// PointwiseMetricResult's "customOutput"/"explanation"/"score" tags).
type evaluateInstancesResponse struct {
	MetricResults []metricResult `json:"metricResults"`
}

type metricResult struct {
	Score       *float64      `json:"score,omitempty"`
	Explanation string        `json:"explanation,omitempty"`
	Error       *responseErr  `json:"error,omitempty"`
	Rubrics     []rubricEntry `json:"rubricVerdicts,omitempty"`
}

type rubricEntry struct {
	RubricID string `json:"rubricId,omitempty"`
	Verdict  bool   `json:"verdict,omitempty"`
}

type responseErr struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (c *Client) evaluateInstances(ctx context.Context, body any) (*metricResult, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding evaluateInstances request: %w", err)
	}

	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/:evaluateInstances",
		c.location, c.project, c.location)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building evaluateInstances request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	tok, err := c.creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting access token: %w", err)
	}
	tokType := tok.Type
	if tokType == "" {
		tokType = "Bearer"
	}
	req.Header.Set("Authorization", tokType+" "+tok.Value)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling evaluateInstances: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading evaluateInstances response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("vertexeval: evaluateInstances HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 2000))
		return nil, fmt.Errorf("evaluateInstances HTTP %d (details are in the server log)", resp.StatusCode)
	}

	var parsed evaluateInstancesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		log.Printf("vertexeval: unparseable evaluateInstances response: %s", truncate(string(respBody), 2000))
		return nil, fmt.Errorf("parsing evaluateInstances response: %w", err)
	}
	if len(parsed.MetricResults) == 0 {
		return nil, fmt.Errorf("evaluateInstances returned no metric results")
	}
	mr := parsed.MetricResults[0]
	if mr.Error != nil && mr.Error.Message != "" {
		return nil, fmt.Errorf("evaluateInstances metric error: %s", mr.Error.Message)
	}
	return &mr, nil
}

// truncate shortens s to at most n bytes for logging.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
