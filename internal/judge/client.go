package judge

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

const (
	DefaultModel      = "gemini-2.5-flash"
	DefaultNumSamples = 5
)

// Model is a single-turn text-in/text-out judge model call, abstracted so
// FinalResponseMatchV2 (and future judge metrics) can be tested against a
// fake without a live API key.
type Model interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// maxJudgeOutputTokens caps one judge reply. A verdict is short, but a thinking model spends its reasoning inside
// the same limit, so it is generous; the point is that no call can run unbounded.
const maxJudgeOutputTokens = 8192

// GenAIModel is a Model backed by the Gemini Developer API via google.golang.org/genai..
type GenAIModel struct {
	client *genai.Client
	model  string
	// onUsage, when set, receives each call's total token count (prompt, response and thinking), for the online
	// evaluation budget.
	onUsage func(tokens int64)
}

// WithUsageHook sets a callback that receives each call's total tokens.
func (m *GenAIModel) WithUsageHook(fn func(tokens int64)) *GenAIModel {
	m.onUsage = fn
	return m
}

// NewGenAIModel creates a judge model client. apiKey may be empty, in which case the underlying SDK
// auto-selects its backend from the environment: GOOGLE_GENAI_USE_VERTEXAI=true routes to Vertex AI using
// GOOGLE_CLOUD_PROJECT/GOOGLE_CLOUD_LOCATION and Application Default Credentials (e.g. a GKE Workload Identity
// service account - no API key anywhere), otherwise it falls back to the Gemini Developer API via
// GEMINI_API_KEY/GOOGLE_API_KEY.
//
// An explicit apiKey (e.g. the CLI's --judge-api-key flag) always forces
// the Gemini Developer API backend, since a caller who hands us a key
// clearly wants it used rather than silently ignored in favor of ADC.
func NewGenAIModel(ctx context.Context, apiKey, model string) (*GenAIModel, error) {
	if model == "" {
		model = DefaultModel
	}
	cfg := &genai.ClientConfig{APIKey: apiKey}
	if apiKey != "" {
		cfg.Backend = genai.BackendGeminiAPI
	}
	client, err := genai.NewClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating genai client: %w", err)
	}
	return &GenAIModel{client: client, model: model}, nil
}

// Generate sends prompt as a single user-role turn and returns the response's concatenated text..
func (m *GenAIModel) Generate(ctx context.Context, prompt string) (string, error) {
	resp, err := m.client.Models.GenerateContent(ctx, m.model,
		[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)},
		&genai.GenerateContentConfig{MaxOutputTokens: maxJudgeOutputTokens},
	)
	if err != nil {
		return "", fmt.Errorf("judge model %s: %w", m.model, err)
	}
	if m.onUsage != nil && resp.UsageMetadata != nil {
		m.onUsage(int64(resp.UsageMetadata.TotalTokenCount))
	}
	return resp.Text(), nil
}
