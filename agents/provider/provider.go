// Package provider defines a unified interface for AI providers (Anthropic, OpenAI, Google).
package provider

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ErrProviderNotAvailable is returned when a provider's API key is not configured.
type ErrProviderNotAvailable string

func (e ErrProviderNotAvailable) Error() string {
	return fmt.Sprintf("provider %s not available: API key not configured", string(e))
}

// Provider is the interface all AI providers must implement.
type Provider interface {
	// CreateMessage sends a message and returns a response.
	CreateMessage(ctx context.Context, req *MessageRequest) (*MessageResponse, error)

	// Name returns the provider name (anthropic, openai, google).
	Name() string

	// Available returns true if the provider's API key is configured.
	Available() bool

	// GetUsage returns token usage statistics.
	GetUsage() TokenUsage

	// ResetUsage clears usage statistics.
	ResetUsage()
}

// MessageRequest is a provider-agnostic message request.
type MessageRequest struct {
	Model         string    // Model identifier (e.g., "claude-sonnet-4-20250514", "gpt-4o")
	MaxTokens     int       // Maximum tokens in response
	System        string    // System prompt
	Messages      []Message // Conversation history
	Temperature   *float64  // Sampling temperature (optional)
	StopSequences []string  // Stop sequences (optional)
}

// Message represents a conversation message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// MessageResponse is a provider-agnostic response.
type MessageResponse struct {
	ID         string        // Response ID
	Content    string        // Text content
	Model      string        // Model used
	StopReason string        // Reason for stopping (e.g., "end_turn", "stop")
	Usage      ResponseUsage // Token usage
}

// ResponseUsage contains token usage from a response.
type ResponseUsage struct {
	InputTokens  int
	OutputTokens int
}

// TokenUsage tracks cumulative token usage.
type TokenUsage struct {
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	TotalRequests int64     `json:"total_requests"`
	LastUsed      time.Time `json:"last_used"`
}

// AgentProviderConfig stores the provider/model configuration for an agent type.
type AgentProviderConfig struct {
	AgentType    string    `json:"agent_type"`              // pm, dev-frontend, qa, etc.
	Provider     string    `json:"provider"`                // anthropic, openai, google
	Model        string    `json:"model"`                   // Model identifier
	SystemPrompt string    `json:"system_prompt,omitempty"` // Custom system prompt override
	UpdatedAt    time.Time `json:"updated_at"`
}

// ProviderInfo describes an available provider and its models.
type ProviderInfo struct { //nolint:revive // keeping explicit name for clarity alongside ModelInfo
	Name        string      `json:"name"`
	DisplayName string      `json:"display_name"` // Human-readable name
	EnvVar      string      `json:"env_var"`      // Environment variable for API key
	Available   bool        `json:"available"`    // API key configured
	Models      []ModelInfo `json:"models"`
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID          string `json:"id"`          // Model identifier for API
	Name        string `json:"name"`        // Human-readable name
	Description string `json:"description"` // Brief description
	Recommended bool   `json:"recommended"` // Recommended default
}

// BaseProvider provides common functionality for providers.
type BaseProvider struct {
	mu    sync.Mutex
	usage TokenUsage
}

// TrackUsage records token usage from a response.
func (b *BaseProvider) TrackUsage(input, output int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.usage.InputTokens += int64(input)
	b.usage.OutputTokens += int64(output)
	b.usage.TotalRequests++
	b.usage.LastUsed = time.Now()
}

// GetUsage returns current token usage statistics.
func (b *BaseProvider) GetUsage() TokenUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage
}

// ResetUsage clears usage statistics.
func (b *BaseProvider) ResetUsage() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage = TokenUsage{}
}

// Model constants for all providers.
const (
	// Anthropic models.
	ModelAnthropicSonnet4 = "claude-sonnet-4-20250514"
	ModelAnthropicHaiku35 = "claude-3-5-haiku-20241022"
	ModelAnthropicOpus45  = "claude-opus-4-5-20251101"

	// OpenAI models.
	ModelOpenAIGPT4o      = "gpt-4o"
	ModelOpenAIGPT4       = "gpt-4"
	ModelOpenAIGPT35Turbo = "gpt-3.5-turbo"

	// Google models.
	ModelGoogleGemini20Flash = "gemini-2.0-flash"
	ModelGoogleGemini15Pro   = "gemini-1.5-pro"
	ModelGoogleGemini15Flash = "gemini-1.5-flash"
)

// DefaultModels returns the default model for each provider.
var DefaultModels = map[string]string{
	"anthropic": ModelAnthropicSonnet4,
	"openai":    ModelOpenAIGPT4o,
	"google":    ModelGoogleGemini20Flash,
}

// AllProviders returns info about all supported providers.
func AllProviders() []ProviderInfo {
	return []ProviderInfo{
		{
			Name:        "anthropic",
			DisplayName: "Anthropic",
			EnvVar:      "ANTHROPIC_API_KEY",
			Models: []ModelInfo{
				{ID: ModelAnthropicSonnet4, Name: "Sonnet 4", Description: "Best quality/cost balance", Recommended: true},
				{ID: ModelAnthropicHaiku35, Name: "Haiku 3.5", Description: "Fast and cost-effective"},
				{ID: ModelAnthropicOpus45, Name: "Opus 4.5", Description: "Most capable"},
			},
		},
		{
			Name:        "openai",
			DisplayName: "OpenAI",
			EnvVar:      "OPENAI_API_KEY",
			Models: []ModelInfo{
				{ID: ModelOpenAIGPT4o, Name: "GPT-4o", Description: "Latest multimodal model", Recommended: true},
				{ID: ModelOpenAIGPT4, Name: "GPT-4", Description: "High capability"},
				{ID: ModelOpenAIGPT35Turbo, Name: "GPT-3.5 Turbo", Description: "Fast and cost-effective"},
			},
		},
		{
			Name:        "google",
			DisplayName: "Google",
			EnvVar:      "GOOGLE_API_KEY",
			Models: []ModelInfo{
				{ID: ModelGoogleGemini20Flash, Name: "Gemini 2.0 Flash", Description: "Latest fast model", Recommended: true},
				{ID: ModelGoogleGemini15Pro, Name: "Gemini 1.5 Pro", Description: "High capability"},
				{ID: ModelGoogleGemini15Flash, Name: "Gemini 1.5 Flash", Description: "Fast and efficient"},
			},
		},
	}
}
