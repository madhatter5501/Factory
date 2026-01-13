package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	openAIBaseURL = "https://api.openai.com/v1"
)

// OpenAIProvider implements the Provider interface for OpenAI.
type OpenAIProvider struct {
	BaseProvider
	apiKey     string
	httpClient *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider() (*OpenAIProvider, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	return &OpenAIProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// Name returns the provider name.
func (p *OpenAIProvider) Name() string {
	return "openai"
}

// Available returns true if the API key is configured.
func (p *OpenAIProvider) Available() bool {
	return p.apiKey != ""
}

// openAIRequest is the request format for OpenAI's chat completions API.
type openAIRequest struct {
	Model       string           `json:"model"`
	Messages    []openAIMessage  `json:"messages"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	Stop        []string         `json:"stop,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIResponse is the response format from OpenAI.
type openAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int           `json:"index"`
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// CreateMessage sends a message to the OpenAI API.
func (p *OpenAIProvider) CreateMessage(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	if !p.Available() {
		return nil, ErrProviderNotAvailable("openai")
	}

	// Build messages array with system message first
	messages := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, openAIMessage{
			Role:    "system",
			Content: req.System,
		})
	}
	for _, msg := range req.Messages {
		messages = append(messages, openAIMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Build request
	openAIReq := openAIRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stop:        req.StopSequences,
	}

	// Set defaults
	if openAIReq.Model == "" {
		openAIReq.Model = ModelOpenAIGPT4o
	}
	if openAIReq.MaxTokens == 0 {
		openAIReq.MaxTokens = 16384
	}

	// Marshal request
	body, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", openAIBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	// Send request
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var openAIResp openAIResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Extract content
	content := ""
	stopReason := ""
	if len(openAIResp.Choices) > 0 {
		content = openAIResp.Choices[0].Message.Content
		stopReason = openAIResp.Choices[0].FinishReason
	}

	// Track usage
	p.TrackUsage(openAIResp.Usage.PromptTokens, openAIResp.Usage.CompletionTokens)

	return &MessageResponse{
		ID:         openAIResp.ID,
		Content:    content,
		Model:      openAIResp.Model,
		StopReason: stopReason,
		Usage: ResponseUsage{
			InputTokens:  openAIResp.Usage.PromptTokens,
			OutputTokens: openAIResp.Usage.CompletionTokens,
		},
	}, nil
}
