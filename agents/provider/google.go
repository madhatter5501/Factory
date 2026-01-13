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
	googleBaseURL = "https://generativelanguage.googleapis.com/v1beta"
)

// GoogleProvider implements the Provider interface for Google Gemini.
type GoogleProvider struct {
	BaseProvider
	apiKey     string
	httpClient *http.Client
}

// NewGoogleProvider creates a new Google Gemini provider.
func NewGoogleProvider() (*GoogleProvider, error) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	return &GoogleProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// Name returns the provider name.
func (p *GoogleProvider) Name() string {
	return "google"
}

// Available returns true if the API key is configured.
func (p *GoogleProvider) Available() bool {
	return p.apiKey != ""
}

// geminiRequest is the request format for Google's Gemini API.
type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int       `json:"maxOutputTokens,omitempty"`
	Temperature     *float64  `json:"temperature,omitempty"`
	StopSequences   []string  `json:"stopSequences,omitempty"`
}

// geminiResponse is the response format from Google Gemini.
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// CreateMessage sends a message to the Google Gemini API.
func (p *GoogleProvider) CreateMessage(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	if !p.Available() {
		return nil, ErrProviderNotAvailable("google")
	}

	model := req.Model
	if model == "" {
		model = ModelGoogleGemini20Flash
	}

	// Build contents array
	contents := make([]geminiContent, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := msg.Role
		// Gemini uses "user" and "model" (not "assistant")
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: msg.Content}},
		})
	}

	// Build request
	geminiReq := geminiRequest{
		Contents: contents,
		GenerationConfig: &geminiGenerationConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
			StopSequences:   req.StopSequences,
		},
	}

	// Add system instruction if provided
	if req.System != "" {
		geminiReq.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: req.System}},
		}
	}

	// Set defaults
	if geminiReq.GenerationConfig.MaxOutputTokens == 0 {
		geminiReq.GenerationConfig.MaxOutputTokens = 8192
	}

	// Marshal request
	body, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build URL with model and API key
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", googleBaseURL, model, p.apiKey)

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

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
		return nil, fmt.Errorf("Google API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Extract content
	content := ""
	stopReason := ""
	if len(geminiResp.Candidates) > 0 {
		candidate := geminiResp.Candidates[0]
		stopReason = candidate.FinishReason
		if len(candidate.Content.Parts) > 0 {
			content = candidate.Content.Parts[0].Text
		}
	}

	// Track usage
	p.TrackUsage(geminiResp.UsageMetadata.PromptTokenCount, geminiResp.UsageMetadata.CandidatesTokenCount)

	return &MessageResponse{
		ID:         "", // Gemini doesn't return an ID
		Content:    content,
		Model:      model,
		StopReason: stopReason,
		Usage: ResponseUsage{
			InputTokens:  geminiResp.UsageMetadata.PromptTokenCount,
			OutputTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
		},
	}, nil
}
