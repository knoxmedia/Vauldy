package aitask

import "context"

// AIProvider defines the interface for AI API calls used by capability subtasks.
type AIProvider interface {
	// ChatCompletion calls the AI provider with a system prompt, user prompt,
	// and JSON mode flag. Returns the raw response content.
	ChatCompletion(ctx context.Context, providerID, apiURL, apiKey, model string, system, user string, jsonMode bool) (string, error)
}

// NoOpProvider is a no-op provider used when no AI backend is configured.
type NoOpProvider struct{}

func (n NoOpProvider) ChatCompletion(ctx context.Context, providerID, apiURL, apiKey, model string, system, user string, jsonMode bool) (string, error) {
	return "", nil
}
