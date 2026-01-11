// Package anthropic provides a client for the Anthropic API with prompt caching support.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	DefaultBaseURL    = "https://api.anthropic.com"
	DefaultAPIVersion = "2023-06-01"
	DefaultModel      = "claude-sonnet-4-20250514"

	// Prompt caching requires beta header
	PromptCachingBeta = "prompt-caching-2024-07-31"
)

// Client provides access to the Anthropic API with prompt caching support.
type Client struct {
	baseURL    string
	apiKey     string
	apiVersion string
	httpClient *http.Client

	// Token usage tracking
	mu       sync.Mutex
	usage    *TokenUsage
	usageLog []UsageEntry
}

// TokenUsage tracks token consumption across requests.
type TokenUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CacheCreationInput int64 `json:"cache_creation_input_tokens"`
	CacheReadInput     int64 `json:"cache_read_input_tokens"`

	// Derived metrics
	TotalRequests    int64   `json:"total_requests"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
	EstimatedSavings float64 `json:"estimated_savings_usd"`
}

// UsageEntry records a single API call's token usage.
type UsageEntry struct {
	Timestamp          time.Time `json:"timestamp"`
	AgentType          string    `json:"agent_type"`
	TicketID           string    `json:"ticket_id,omitempty"`
	InputTokens        int       `json:"input_tokens"`
	OutputTokens       int       `json:"output_tokens"`
	CacheCreationInput int       `json:"cache_creation_input_tokens"`
	CacheReadInput     int       `json:"cache_read_input_tokens"`
	DurationMs         int64     `json:"duration_ms"`
}

// ClientOption configures the client.
type ClientOption func(*Client)

// WithBaseURL sets a custom base URL.
func WithBaseURL(url string) ClientOption {
	return func(c *Client) {
		c.baseURL = url
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = client
	}
}

// NewClient creates a new Anthropic API client.
func NewClient(apiKey string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		apiKey:     apiKey,
		apiVersion: DefaultAPIVersion,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		usage:      &TokenUsage{},
		usageLog:   make([]UsageEntry, 0),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// NewClientFromEnv creates a client using ANTHROPIC_API_KEY env var.
func NewClientFromEnv(opts ...ClientOption) (*Client, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}
	return NewClient(apiKey, opts...), nil
}

// Message represents a conversation message.
type Message struct {
	Role    string        `json:"role"` // "user" or "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock represents a block of content in a message.
type ContentBlock struct {
	Type         string        `json:"type"` // "text" or "tool_use" or "tool_result"
	Text         string        `json:"text,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`

	// Tool use fields
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// Tool result fields
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// CacheControl specifies caching behavior for a content block.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral" - cached for 5 minutes
}

// Ephemeral returns a cache control for ephemeral caching.
func Ephemeral() *CacheControl {
	return &CacheControl{Type: "ephemeral"}
}

// SystemBlock represents a system prompt block with optional caching.
type SystemBlock struct {
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// CreateMessageRequest is the request body for creating a message.
type CreateMessageRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    []SystemBlock  `json:"system,omitempty"`
	Messages  []Message      `json:"messages"`

	// Optional
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
}

// CreateMessageResponse is the response from creating a message.
type CreateMessageResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence string         `json:"stop_sequence,omitempty"`
	Usage        ResponseUsage  `json:"usage"`
}

// ResponseUsage contains token usage from a response.
type ResponseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	CacheCreationInput int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInput     int `json:"cache_read_input_tokens,omitempty"`
}

// GetText returns the concatenated text content from the response.
func (r *CreateMessageResponse) GetText() string {
	var result string
	for _, block := range r.Content {
		if block.Type == "text" {
			result += block.Text
		}
	}
	return result
}

// CreateMessage sends a message to the API.
func (c *Client) CreateMessage(ctx context.Context, req *CreateMessageRequest) (*CreateMessageResponse, error) {
	if req.Model == "" {
		req.Model = DefaultModel
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 16384
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", c.apiVersion)
	httpReq.Header.Set("anthropic-beta", PromptCachingBeta)

	start := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	duration := time.Since(start)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var msgResp CreateMessageResponse
	if err := json.Unmarshal(respBody, &msgResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Track usage
	c.trackUsage("", "", &msgResp.Usage, duration)

	return &msgResp, nil
}

// CreateMessageWithTracking sends a message and tracks usage with agent info.
func (c *Client) CreateMessageWithTracking(
	ctx context.Context,
	req *CreateMessageRequest,
	agentType, ticketID string,
) (*CreateMessageResponse, error) {
	if req.Model == "" {
		req.Model = DefaultModel
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 16384
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", c.apiVersion)
	httpReq.Header.Set("anthropic-beta", PromptCachingBeta)

	start := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	duration := time.Since(start)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var msgResp CreateMessageResponse
	if err := json.Unmarshal(respBody, &msgResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Track usage with agent info
	c.trackUsage(agentType, ticketID, &msgResp.Usage, duration)

	return &msgResp, nil
}

// trackUsage records token usage from a response.
func (c *Client) trackUsage(agentType, ticketID string, usage *ResponseUsage, duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := UsageEntry{
		Timestamp:          time.Now(),
		AgentType:          agentType,
		TicketID:           ticketID,
		InputTokens:        usage.InputTokens,
		OutputTokens:       usage.OutputTokens,
		CacheCreationInput: usage.CacheCreationInput,
		CacheReadInput:     usage.CacheReadInput,
		DurationMs:         duration.Milliseconds(),
	}
	c.usageLog = append(c.usageLog, entry)

	// Update aggregates
	c.usage.InputTokens += int64(usage.InputTokens)
	c.usage.OutputTokens += int64(usage.OutputTokens)
	c.usage.CacheCreationInput += int64(usage.CacheCreationInput)
	c.usage.CacheReadInput += int64(usage.CacheReadInput)
	c.usage.TotalRequests++

	// Calculate cache hit rate
	totalCacheTokens := c.usage.CacheCreationInput + c.usage.CacheReadInput
	if totalCacheTokens > 0 {
		c.usage.CacheHitRate = float64(c.usage.CacheReadInput) / float64(totalCacheTokens)
	}

	// Estimate savings (cache reads are 90% cheaper)
	// Regular input: $0.003/1K tokens, Cached read: $0.0003/1K tokens
	savedTokens := float64(c.usage.CacheReadInput) * 0.9
	c.usage.EstimatedSavings = savedTokens * 0.003 / 1000
}

// GetUsage returns current token usage statistics.
func (c *Client) GetUsage() TokenUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.usage
}

// GetUsageLog returns all usage entries.
func (c *Client) GetUsageLog() []UsageEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]UsageEntry, len(c.usageLog))
	copy(result, c.usageLog)
	return result
}

// ResetUsage clears usage statistics.
func (c *Client) ResetUsage() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage = &TokenUsage{}
	c.usageLog = make([]UsageEntry, 0)
}
