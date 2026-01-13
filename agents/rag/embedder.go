package rag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder generates vector embeddings for text content.
type Embedder struct {
	apiKey     string
	httpClient *http.Client
	model      string
}

// EmbedderOption configures the embedder.
type EmbedderOption func(*Embedder)

// WithEmbeddingModel sets the embedding model.
func WithEmbeddingModel(model string) EmbedderOption {
	return func(e *Embedder) {
		e.model = model
	}
}

// NewEmbedder creates a new embedder using Voyage AI (recommended for code).
// Falls back to a simple hash-based approach if no API key is available.
func NewEmbedder(opts ...EmbedderOption) (*Embedder, error) {
	e := &Embedder{
		apiKey:     os.Getenv("VOYAGE_API_KEY"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		model:      "voyage-code-2", // Best for code
	}

	for _, opt := range opts {
		opt(e)
	}

	return e, nil
}

// Embed generates an embedding for a single text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}
	return embeddings[0], nil
}

// EmbedBatch generates embeddings for multiple texts.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if e.apiKey == "" {
		// Fallback to simple hash-based embeddings for development
		return e.hashEmbeddings(texts), nil
	}

	return e.voyageEmbeddings(ctx, texts)
}

// voyageEmbeddings uses Voyage AI API for high-quality code embeddings.
func (e *Embedder) voyageEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := map[string]interface{}{
		"input":      texts,
		"model":      e.model,
		"input_type": "document",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.voyageai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	embeddings := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		embeddings[i] = d.Embedding
	}

	return embeddings, nil
}

// hashEmbeddings creates simple hash-based embeddings for development/testing.
// This provides basic functionality without requiring an embedding API.
func (e *Embedder) hashEmbeddings(texts []string) [][]float32 {
	const dimensions = 256 // Smaller dimension for hash embeddings

	embeddings := make([][]float32, len(texts))
	for i, text := range texts {
		embeddings[i] = e.textToHashVector(text, dimensions)
	}
	return embeddings
}

// textToHashVector creates a deterministic vector from text using hashing.
// This is NOT as good as learned embeddings but provides reasonable similarity.
func (e *Embedder) textToHashVector(text string, dimensions int) []float32 {
	// Normalize text
	text = strings.ToLower(strings.TrimSpace(text))

	// Extract features (words, bigrams)
	words := strings.Fields(text)
	features := make(map[string]int)

	// Unigrams
	for _, word := range words {
		features[word]++
	}

	// Bigrams
	for i := 0; i < len(words)-1; i++ {
		bigram := words[i] + " " + words[i+1]
		features[bigram]++
	}

	// Create vector using feature hashing
	vector := make([]float32, dimensions)
	var magnitude float64

	for feature, count := range features {
		hash := sha256.Sum256([]byte(feature))
		// Use first 4 bytes for index, next 4 for sign.
		idx := int(hash[0])<<8 | int(hash[1])
		idx %= dimensions
		sign := float32(1.0)
		if hash[4]&1 == 1 {
			sign = -1.0
		}

		vector[idx] += sign * float32(count)
		magnitude += float64(vector[idx] * vector[idx])
	}

	// Normalize
	if magnitude > 0 {
		magnitude = 1.0 / float64(magnitude)
		for i := range vector {
			vector[i] *= float32(magnitude)
		}
	}

	return vector
}

// ChunkText splits text into chunks for embedding.
func ChunkText(text string, maxTokens int) []string {
	// Simple chunking by paragraphs, respecting token limits
	// Approximate tokens as words * 1.3

	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	var currentChunk strings.Builder
	currentTokens := 0

	for _, para := range paragraphs {
		paraTokens := estimateTokens(para)

		if currentTokens+paraTokens > maxTokens && currentChunk.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(currentChunk.String()))
			currentChunk.Reset()
			currentTokens = 0
		}

		if paraTokens > maxTokens {
			// Split large paragraph by sentences
			sentences := splitSentences(para)
			for _, sent := range sentences {
				sentTokens := estimateTokens(sent)
				if currentTokens+sentTokens > maxTokens && currentChunk.Len() > 0 {
					chunks = append(chunks, strings.TrimSpace(currentChunk.String()))
					currentChunk.Reset()
					currentTokens = 0
				}
				currentChunk.WriteString(sent)
				currentChunk.WriteString(" ")
				currentTokens += sentTokens
			}
		} else {
			currentChunk.WriteString(para)
			currentChunk.WriteString("\n\n")
			currentTokens += paraTokens
		}
	}

	if currentChunk.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(currentChunk.String()))
	}

	return chunks
}

// estimateTokens provides a rough token count.
func estimateTokens(text string) int {
	words := len(strings.Fields(text))
	return int(float64(words) * 1.3)
}

// splitSentences splits text into sentences.
func splitSentences(text string) []string {
	// Simple sentence splitting
	var sentences []string
	var current strings.Builder

	for _, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			sentences = append(sentences, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}

	if current.Len() > 0 {
		sentences = append(sentences, strings.TrimSpace(current.String()))
	}

	return sentences
}

// GenerateChunkID creates a unique ID for a chunk.
func GenerateChunkID(source, content string) string {
	hash := sha256.Sum256([]byte(source + content))
	return hex.EncodeToString(hash[:8])
}
