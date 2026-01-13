package provider

import (
	"context"
	"os"

	"github.com/madhatter5501/Factory/agents/anthropic"
)

// AnthropicProvider wraps the Anthropic client to implement the Provider interface.
type AnthropicProvider struct {
	BaseProvider
	client *anthropic.Client
	apiKey string
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider() (*AnthropicProvider, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		// Return provider even without key (Available() will return false)
		return &AnthropicProvider{apiKey: ""}, nil
	}

	client := anthropic.NewClient(apiKey)
	return &AnthropicProvider{
		client: client,
		apiKey: apiKey,
	}, nil
}

// Name returns the provider name.
func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

// Available returns true if the API key is configured.
func (p *AnthropicProvider) Available() bool {
	return p.apiKey != ""
}

// CreateMessage sends a message to the Anthropic API.
func (p *AnthropicProvider) CreateMessage(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	if p.client == nil {
		return nil, ErrProviderNotAvailable("anthropic")
	}

	// Convert to Anthropic request format
	anthropicReq := &anthropic.CreateMessageRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System: []anthropic.SystemBlock{
			{Type: "text", Text: req.System},
		},
		Messages:      convertToAnthropicMessages(req.Messages),
		Temperature:   req.Temperature,
		StopSequences: req.StopSequences,
	}

	// Set defaults
	if anthropicReq.Model == "" {
		anthropicReq.Model = ModelAnthropicSonnet4
	}
	if anthropicReq.MaxTokens == 0 {
		anthropicReq.MaxTokens = 16384
	}

	// Call API
	resp, err := p.client.CreateMessage(ctx, anthropicReq)
	if err != nil {
		return nil, err
	}

	// Track usage
	p.TrackUsage(resp.Usage.InputTokens, resp.Usage.OutputTokens)

	// Convert response
	return &MessageResponse{
		ID:         resp.ID,
		Content:    resp.GetText(),
		Model:      resp.Model,
		StopReason: resp.StopReason,
		Usage: ResponseUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

// GetClient returns the underlying Anthropic client for advanced usage.
func (p *AnthropicProvider) GetClient() *anthropic.Client {
	return p.client
}

// convertToAnthropicMessages converts provider messages to Anthropic format.
func convertToAnthropicMessages(messages []Message) []anthropic.Message {
	result := make([]anthropic.Message, len(messages))
	for i, msg := range messages {
		result[i] = anthropic.Message{
			Role: msg.Role,
			Content: []anthropic.ContentBlock{
				{Type: "text", Text: msg.Content},
			},
		}
	}
	return result
}
