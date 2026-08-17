// Package ai defines a provider-agnostic interface for the LLM that powers
// ContriAI's analysis. Phase 1 ships two implementations: Ollama (local
// models, no source code ever leaves the machine) and a deterministic Mock
// provider used when no local model is configured, so the tool still
// produces a useful (if less nuanced) analysis out of the box.
package ai

import "context"

// Provider is anything that can turn a prompt into a completion. Swapping
// providers (Ollama today, OpenAI/Anthropic/etc. later) never requires
// touching internal/analysis.
type Provider interface {
	// Name identifies the provider for display/logging, e.g. "ollama:llama3.2".
	Name() string
	// Complete sends prompt to the model and returns its raw text response.
	Complete(ctx context.Context, prompt string) (string, error)
}
