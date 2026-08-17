package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaProvider talks to a locally running Ollama daemon
// (https://ollama.com), so repository/issue content never leaves the
// developer's machine. This is the provider ContriAI defaults to.
type OllamaProvider struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOllamaProvider builds a provider targeting the given Ollama model.
// baseURL defaults to http://localhost:11434 when empty.
func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaProvider{
		baseURL:    baseURL,
		model:      model,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *OllamaProvider) Name() string {
	return "ollama:" + p.model
}

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	Format string `json:"format,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

func (p *OllamaProvider) Complete(ctx context.Context, prompt string) (string, error) {
	reqBody, err := json.Marshal(ollamaGenerateRequest{
		Model:  p.model,
		Prompt: prompt,
		Stream: false,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/generate", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach Ollama at %s (is `ollama serve` running?): %w", p.baseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("ollama returned %d: %s", resp.StatusCode, string(body))
	}

	var out ollamaGenerateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decoding ollama response: %w", err)
	}
	return out.Response, nil
}
