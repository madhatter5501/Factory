package rag

import (
	"context"
	"fmt"
	"strings"
)

// Retriever provides context retrieval for agent prompts.
type Retriever struct {
	store    *VectorStore
	embedder *Embedder
}

// NewRetriever creates a new context retriever.
func NewRetriever(store *VectorStore, embedder *Embedder) *Retriever {
	return &Retriever{
		store:    store,
		embedder: embedder,
	}
}

// RetrievedContext represents retrieved context for an agent prompt.
type RetrievedContext struct {
	Patterns     []RetrievedItem `json:"patterns"`
	CodeExamples []RetrievedItem `json:"code_examples"`
	History      []RetrievedItem `json:"history"`
	TotalTokens  int             `json:"total_tokens"`
}

// RetrievedItem represents a single retrieved piece of context.
type RetrievedItem struct {
	Source     string  `json:"source"`
	Content    string  `json:"content"`
	Similarity float64 `json:"similarity"`
	Tokens     int     `json:"tokens"`
}

// RetrieveForTicket retrieves relevant context for a ticket.
func (r *Retriever) RetrieveForTicket(ctx context.Context, ticket TicketContext, opts RetrievalOptions) (*RetrievedContext, error) {
	result := &RetrievedContext{
		Patterns:     make([]RetrievedItem, 0),
		CodeExamples: make([]RetrievedItem, 0),
		History:      make([]RetrievedItem, 0),
	}

	// Build query from ticket context
	queryText := r.buildQueryText(ticket)

	// Generate query embedding
	queryEmbedding, err := r.embedder.Embed(ctx, queryText)
	if err != nil {
		// Fallback to keyword search
		return r.keywordFallback(ctx, queryText, ticket, opts)
	}

	// Retrieve patterns for the domain
	if ticket.Domain != "" {
		searchOpts := SearchOptions{
			Limit:         opts.MaxPatterns,
			MinSimilarity: 0.5,
			Domain:        ticket.Domain,
			ChunkType:     "pattern",
		}

		patterns, err := r.store.Search(ctx, queryEmbedding, searchOpts)
		if err == nil {
			for _, p := range patterns {
				item := RetrievedItem{
					Source:     p.Chunk.Source,
					Content:    p.Chunk.Content,
					Similarity: p.Similarity,
					Tokens:     p.Chunk.Metadata.TokenCount,
				}
				result.Patterns = append(result.Patterns, item)
				result.TotalTokens += item.Tokens
			}
		}
	}

	// Retrieve code examples
	searchOpts := SearchOptions{
		Limit:         opts.MaxCodeExamples,
		MinSimilarity: 0.4,
		ChunkType:     "code",
	}
	if ticket.Domain != "" {
		searchOpts.Domain = ticket.Domain
	}

	codeResults, err := r.store.Search(ctx, queryEmbedding, searchOpts)
	if err == nil {
		for _, c := range codeResults {
			// Skip if we're over token budget
			if result.TotalTokens+c.Chunk.Metadata.TokenCount > opts.MaxTokens {
				break
			}

			item := RetrievedItem{
				Source:     c.Chunk.Source,
				Content:    c.Chunk.Content,
				Similarity: c.Similarity,
				Tokens:     c.Chunk.Metadata.TokenCount,
			}
			result.CodeExamples = append(result.CodeExamples, item)
			result.TotalTokens += item.Tokens
		}
	}

	return result, nil
}

// TicketContext contains ticket information for retrieval.
type TicketContext struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Domain      string   `json:"domain"`
	Stack       []string `json:"stack"`
	Keywords    []string `json:"keywords"`
}

// RetrievalOptions configures context retrieval.
type RetrievalOptions struct {
	MaxPatterns     int `json:"max_patterns"`
	MaxCodeExamples int `json:"max_code_examples"`
	MaxHistory      int `json:"max_history"`
	MaxTokens       int `json:"max_tokens"`
}

// DefaultRetrievalOptions returns sensible defaults.
func DefaultRetrievalOptions() RetrievalOptions {
	return RetrievalOptions{
		MaxPatterns:     5,
		MaxCodeExamples: 3,
		MaxHistory:      2,
		MaxTokens:       2000, // Keep retrieved context under 2k tokens
	}
}

// buildQueryText creates a search query from ticket context.
func (r *Retriever) buildQueryText(ticket TicketContext) string {
	var parts []string

	if ticket.Title != "" {
		parts = append(parts, ticket.Title)
	}
	if ticket.Description != "" {
		// Truncate long descriptions
		desc := ticket.Description
		if len(desc) > 500 {
			desc = desc[:500]
		}
		parts = append(parts, desc)
	}
	if ticket.Domain != "" {
		parts = append(parts, ticket.Domain)
	}
	parts = append(parts, ticket.Stack...)
	parts = append(parts, ticket.Keywords...)

	return strings.Join(parts, " ")
}

// keywordFallback uses keyword search when embeddings fail.
func (r *Retriever) keywordFallback(ctx context.Context, query string, _ TicketContext, opts RetrievalOptions) (*RetrievedContext, error) {
	result := &RetrievedContext{
		Patterns:     make([]RetrievedItem, 0),
		CodeExamples: make([]RetrievedItem, 0),
		History:      make([]RetrievedItem, 0),
	}

	// Extract keywords from query
	keywords := extractKeywords(query)
	if len(keywords) == 0 {
		return result, nil
	}

	searchOpts := SearchOptions{
		Limit: opts.MaxPatterns + opts.MaxCodeExamples,
	}

	results, err := r.store.SearchKeyword(ctx, strings.Join(keywords, " OR "), searchOpts)
	if err != nil {
		return result, err
	}

	for _, r := range results {
		if result.TotalTokens+r.Chunk.Metadata.TokenCount > opts.MaxTokens {
			break
		}

		item := RetrievedItem{
			Source:     r.Chunk.Source,
			Content:    r.Chunk.Content,
			Similarity: r.Similarity,
			Tokens:     r.Chunk.Metadata.TokenCount,
		}

		if r.Chunk.Metadata.ChunkType == "code" {
			result.CodeExamples = append(result.CodeExamples, item)
		} else {
			result.Patterns = append(result.Patterns, item)
		}
		result.TotalTokens += item.Tokens
	}

	return result, nil
}

// extractKeywords extracts important keywords from text.
func extractKeywords(text string) []string {
	// Simple keyword extraction - remove common words
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "shall": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "at": true, "by": true, "from": true, "as": true,
		"into": true, "through": true, "during": true, "before": true,
		"after": true, "above": true, "below": true, "between": true,
		"and": true, "or": true, "but": true, "if": true, "then": true,
		"else": true, "when": true, "where": true, "why": true, "how": true,
		"all": true, "each": true, "every": true, "both": true, "few": true,
		"more": true, "most": true, "other": true, "some": true, "such": true,
		"no": true, "nor": true, "not": true, "only": true, "own": true,
		"same": true, "so": true, "than": true, "too": true, "very": true,
		"just": true, "can": true, "this": true, "that": true, "these": true,
		"those": true, "it": true, "its": true, "i": true, "you": true,
		"we": true, "they": true, "he": true, "she": true, "them": true,
	}

	words := strings.Fields(strings.ToLower(text))
	var keywords []string

	for _, word := range words {
		// Remove punctuation
		word = strings.Trim(word, ".,;:!?()[]{}\"'")

		if len(word) < 3 {
			continue
		}
		if stopWords[word] {
			continue
		}

		keywords = append(keywords, word)
	}

	// Limit to most important keywords
	if len(keywords) > 20 {
		keywords = keywords[:20]
	}

	return keywords
}

// RetrieveExpertKnowledge retrieves relevant expert knowledge for a domain.
func (r *Retriever) RetrieveExpertKnowledge(ctx context.Context, domain string, question string, maxTokens int) ([]RetrievedItem, error) {
	// Generate embedding for the question
	embedding, err := r.embedder.Embed(ctx, question)
	if err != nil {
		// Fallback to keyword search
		return r.keywordExpertSearch(ctx, domain, question, maxTokens)
	}

	searchOpts := SearchOptions{
		Limit:         10,
		MinSimilarity: 0.4,
		Domain:        domain,
	}

	results, err := r.store.Search(ctx, embedding, searchOpts)
	if err != nil {
		return nil, err
	}

	var items []RetrievedItem
	totalTokens := 0

	for _, result := range results {
		if totalTokens+result.Chunk.Metadata.TokenCount > maxTokens {
			break
		}

		items = append(items, RetrievedItem{
			Source:     result.Chunk.Source,
			Content:    result.Chunk.Content,
			Similarity: result.Similarity,
			Tokens:     result.Chunk.Metadata.TokenCount,
		})
		totalTokens += result.Chunk.Metadata.TokenCount
	}

	return items, nil
}

func (r *Retriever) keywordExpertSearch(ctx context.Context, domain, question string, maxTokens int) ([]RetrievedItem, error) {
	keywords := extractKeywords(question)
	if len(keywords) == 0 {
		return nil, nil
	}

	searchOpts := SearchOptions{
		Limit:  10,
		Domain: domain,
	}

	results, err := r.store.SearchKeyword(ctx, strings.Join(keywords, " OR "), searchOpts)
	if err != nil {
		return nil, err
	}

	var items []RetrievedItem
	totalTokens := 0

	for _, result := range results {
		if totalTokens+result.Chunk.Metadata.TokenCount > maxTokens {
			break
		}

		items = append(items, RetrievedItem{
			Source:     result.Chunk.Source,
			Content:    result.Chunk.Content,
			Similarity: result.Similarity,
			Tokens:     result.Chunk.Metadata.TokenCount,
		})
		totalTokens += result.Chunk.Metadata.TokenCount
	}

	return items, nil
}

// FormatRetrievedContext formats retrieved context for inclusion in prompts.
func FormatRetrievedContext(rc *RetrievedContext) string {
	if rc == nil || (len(rc.Patterns) == 0 && len(rc.CodeExamples) == 0) {
		return ""
	}

	var sb strings.Builder

	if len(rc.Patterns) > 0 {
		sb.WriteString("## Relevant Patterns\n\n")
		for _, p := range rc.Patterns {
			sb.WriteString(fmt.Sprintf("### From %s (relevance: %.0f%%)\n\n", p.Source, p.Similarity*100))
			sb.WriteString(p.Content)
			sb.WriteString("\n\n")
		}
	}

	if len(rc.CodeExamples) > 0 {
		sb.WriteString("## Relevant Code Examples\n\n")
		for _, c := range rc.CodeExamples {
			sb.WriteString(fmt.Sprintf("### From %s (relevance: %.0f%%)\n\n", c.Source, c.Similarity*100))
			sb.WriteString("```\n")
			sb.WriteString(c.Content)
			sb.WriteString("\n```\n\n")
		}
	}

	return sb.String()
}
