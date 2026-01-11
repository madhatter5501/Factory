package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/madhatter5501/Factory/agents/anthropic"
	"github.com/madhatter5501/Factory/agents/rag"
	"github.com/madhatter5501/Factory/kanban"
)

// APISpawner manages agent spawning via direct Anthropic API calls with prompt caching.
type APISpawner struct {
	client        *anthropic.Client
	promptBuilder *anthropic.PromptBuilder
	summarizer    *ConversationSummarizer
	retriever     *RAGRetriever
	timeout       time.Duration
	verbose       bool
	model         string
}

// APISpawnerConfig configures the API spawner.
type APISpawnerConfig struct {
	PromptsDir string
	Timeout    time.Duration
	Verbose    bool
	Model      string // Optional model override

	// RAG configuration
	RAGEnabled   bool
	VectorDBPath string
}

// NewAPISpawner creates a new API-based agent spawner.
func NewAPISpawner(cfg APISpawnerConfig) (*APISpawner, error) {
	client, err := anthropic.NewClientFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create Anthropic client: %w", err)
	}

	promptBuilder, err := anthropic.NewPromptBuilder(cfg.PromptsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create prompt builder: %w", err)
	}

	s := &APISpawner{
		client:        client,
		promptBuilder: promptBuilder,
		summarizer:    NewConversationSummarizer(client),
		timeout:       cfg.Timeout,
		verbose:       cfg.Verbose,
		model:         cfg.Model,
	}

	// Initialize RAG if enabled
	if cfg.RAGEnabled && cfg.VectorDBPath != "" {
		retriever, err := NewRAGRetriever(cfg.VectorDBPath)
		if err != nil {
			// Non-fatal - continue without RAG
			if s.verbose {
				fmt.Printf("[api-spawner] RAG initialization failed, continuing without: %v\n", err)
			}
		} else {
			s.retriever = retriever
		}
	}

	return s, nil
}

// SpawnAgent runs an agent using the Anthropic API with prompt caching.
func (s *APISpawner) SpawnAgent(ctx context.Context, agentType AgentType, data PromptData, workDir string) (*AgentResult, error) {
	startTime := time.Now()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Convert to API prompt data
	promptData := s.convertPromptData(data, agentType)

	// Summarize conversation if multi-round PRD
	if data.Conversation != nil && data.CurrentRound > 1 {
		summary, err := s.summarizer.SummarizeConversation(ctx, data.Conversation, data.CurrentRound)
		if err != nil {
			if s.verbose {
				fmt.Printf("[api-spawner] Failed to summarize conversation: %v\n", err)
			}
		} else {
			promptData.ConversationSummary = summary
		}
	}

	// Retrieve relevant patterns via RAG if available
	if s.retriever != nil && data.Ticket != nil {
		patterns, err := s.retriever.RetrievePatterns(ctx, data.Ticket, promptData.Domain)
		if err != nil {
			if s.verbose {
				fmt.Printf("[api-spawner] RAG retrieval failed: %v\n", err)
			}
		} else {
			promptData.RetrievedPatterns = formatRetrievedChunks(patterns)
		}
	}

	// Build cached prompt
	parts, err := s.promptBuilder.BuildCachedPrompt(string(agentType), promptData)
	if err != nil {
		return &AgentResult{
			Success:   false,
			AgentType: agentType,
			Error:     fmt.Sprintf("failed to build prompt: %v", err),
		}, err
	}

	// Create API request with system blocks
	systemBlocks := s.promptBuilder.BuildSystemBlocks(parts)
	req := &anthropic.CreateMessageRequest{
		Model:     s.model,
		MaxTokens: 16384,
		System:    systemBlocks,
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: []anthropic.ContentBlock{
					{
						Type: "text",
						Text: "Execute the task described in the system prompt. Output your results as specified.",
					},
				},
			},
		},
	}

	// Send request with tracking
	ticketID := ""
	if data.Ticket != nil {
		ticketID = data.Ticket.ID
	}

	resp, err := s.client.CreateMessageWithTracking(ctx, req, string(agentType), ticketID)
	if err != nil {
		return &AgentResult{
			Success:   false,
			AgentType: agentType,
			TicketID:  ticketID,
			Error:     fmt.Sprintf("API call failed: %v", err),
			Duration:  time.Since(startTime),
		}, err
	}

	// Extract output
	output := resp.GetText()

	result := &AgentResult{
		Success:   true,
		AgentType: agentType,
		TicketID:  ticketID,
		Output:    output,
		Duration:  time.Since(startTime),
		ExitCode:  0,
	}

	// Check for standard markers
	if strings.Contains(output, `"status": "failed"`) {
		result.Success = false
	}
	if strings.Contains(output, `"status": "needs-review"`) {
		result.Success = true // Still counts as success, just needs review
	}

	if s.verbose {
		usage := s.client.GetUsage()
		fmt.Printf("[api-spawner] %s completed in %v (cache hit rate: %.1f%%, saved: $%.4f)\n",
			agentType, result.Duration, usage.CacheHitRate*100, usage.EstimatedSavings)
	}

	return result, nil
}

// convertPromptData converts the existing PromptData to the API format.
func (s *APISpawner) convertPromptData(data PromptData, agentType AgentType) anthropic.AgentPromptData {
	promptData := anthropic.AgentPromptData{
		Ticket:           data.Ticket,
		WorktreePath:     data.WorktreePath,
		BoardStats:       data.BoardStats,
		Iteration:        data.Iteration,
		AgentName:        string(agentType),
		Domain:           data.Domain,
		Questions:        data.Questions,
		ConsultationJSON: data.ConsultationJSON,
		ExtraContext:     data.ExtraContext,
		CurrentRound:     data.CurrentRound,
		CurrentPrompt:    data.CurrentPrompt,
		Agent:            data.Agent,
		FocusAreas:       data.FocusAreas,
		PRD:              data.PRD,
	}

	// Convert AllTickets to interface slice
	if len(data.AllTickets) > 0 {
		allTickets := make([]interface{}, len(data.AllTickets))
		for i, t := range data.AllTickets {
			allTickets[i] = t
		}
		promptData.AllTickets = allTickets
	}

	// Create minimal ticket JSON for the agent type
	if data.Ticket != nil {
		minimalJSON, err := anthropic.MinimalTicketJSON(data.Ticket, string(agentType))
		if err == nil {
			promptData.TicketJSON = minimalJSON
		} else {
			// Fallback to full JSON
			ticketJSON, _ := json.MarshalIndent(data.Ticket, "", "  ")
			promptData.TicketJSON = string(ticketJSON)
		}
	}

	// Convert conversation for PRD agents
	if data.Conversation != nil {
		promptData.Conversation = data.Conversation
	}

	// Convert final expert inputs
	if data.FinalExpertInputs != nil {
		promptData.FinalExpertInputs = data.FinalExpertInputs
	}

	return promptData
}

// GetUsage returns current token usage statistics.
func (s *APISpawner) GetUsage() anthropic.TokenUsage {
	return s.client.GetUsage()
}

// GetUsageLog returns all usage entries.
func (s *APISpawner) GetUsageLog() []anthropic.UsageEntry {
	return s.client.GetUsageLog()
}

// ResetUsage clears usage statistics.
func (s *APISpawner) ResetUsage() {
	s.client.ResetUsage()
}

// ValidateAgentEnvironment checks that the API spawner is properly configured.
func (s *APISpawner) ValidateAgentEnvironment() []string {
	var errors []string

	// Check API client
	if s.client == nil {
		errors = append(errors, "Anthropic API client not initialized")
	}

	// Check prompt builder
	if s.promptBuilder == nil {
		errors = append(errors, "Prompt builder not initialized")
	}

	return errors
}

// ConversationSummarizer creates summaries of PRD conversations.
type ConversationSummarizer struct {
	client *anthropic.Client
}

// NewConversationSummarizer creates a new conversation summarizer.
func NewConversationSummarizer(client *anthropic.Client) *ConversationSummarizer {
	return &ConversationSummarizer{client: client}
}

// SummarizeConversation creates a summary of the PRD conversation up to the current round.
func (cs *ConversationSummarizer) SummarizeConversation(
	ctx context.Context,
	conversation *kanban.PRDConversation,
	currentRound int,
) (string, error) {
	if conversation == nil || len(conversation.Rounds) == 0 {
		return "", nil
	}

	// Build conversation text for summarization
	var convText strings.Builder
	for i, round := range conversation.Rounds {
		if i >= currentRound-1 {
			break // Don't summarize the current round
		}
		convText.WriteString(fmt.Sprintf("Round %d:\n", round.RoundNumber))
		convText.WriteString(fmt.Sprintf("PM: %s\n", round.PMPrompt))
		for agent, input := range round.ExpertInputs {
			convText.WriteString(fmt.Sprintf("%s: %s\n", agent, truncate(input.Response, 500)))
		}
		convText.WriteString("\n")
	}

	if convText.Len() == 0 {
		return "", nil
	}

	// Use a smaller, faster model for summarization
	req := &anthropic.CreateMessageRequest{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 1024,
		System: []anthropic.SystemBlock{
			{
				Type: "text",
				Text: `You are a conversation summarizer. Create a concise summary of the PRD discussion that captures:
1. Key decisions made
2. Open questions or concerns
3. Areas of agreement/disagreement
4. Critical requirements identified

Be extremely concise. Use bullet points. Maximum 300 words.`,
			},
		},
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: []anthropic.ContentBlock{
					{Type: "text", Text: convText.String()},
				},
			},
		},
	}

	resp, err := cs.client.CreateMessage(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.GetText(), nil
}

// truncate shortens a string to maxLen with ellipsis.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// formatRetrievedChunks converts retrieved chunks into a formatted markdown string.
func formatRetrievedChunks(chunks []anthropic.RetrievedChunk) string {
	if len(chunks) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, chunk := range chunks {
		sb.WriteString(fmt.Sprintf("### From %s (relevance: %.2f)\n\n", chunk.Source, chunk.Similarity))
		sb.WriteString(chunk.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// RAGRetriever retrieves relevant context using vector similarity search.
type RAGRetriever struct {
	store     *rag.VectorStore
	retriever *rag.Retriever
}

// NewRAGRetriever creates a new RAG retriever.
func NewRAGRetriever(dbPath string) (*RAGRetriever, error) {
	store, err := rag.NewVectorStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create vector store: %w", err)
	}

	embedder, err := rag.NewEmbedder()
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to create embedder: %w", err)
	}

	retriever := rag.NewRetriever(store, embedder)

	return &RAGRetriever{
		store:     store,
		retriever: retriever,
	}, nil
}

// RetrievePatterns retrieves relevant code patterns for a ticket.
func (r *RAGRetriever) RetrievePatterns(
	ctx context.Context,
	ticket *kanban.Ticket,
	domain string,
) ([]anthropic.RetrievedChunk, error) {
	if r.retriever == nil {
		return nil, nil
	}

	// Build ticket context for retrieval
	ticketCtx := rag.TicketContext{
		ID:          ticket.ID,
		Title:       ticket.Title,
		Description: ticket.Description,
		Domain:      domain,
	}

	// Add domain as a keyword if available
	if string(ticket.Domain) != "" {
		ticketCtx.Keywords = append(ticketCtx.Keywords, string(ticket.Domain))
	}

	opts := rag.DefaultRetrievalOptions()
	retrieved, err := r.retriever.RetrieveForTicket(ctx, ticketCtx, opts)
	if err != nil {
		return nil, err
	}

	// Convert to anthropic.RetrievedChunk format
	var chunks []anthropic.RetrievedChunk

	for _, p := range retrieved.Patterns {
		chunks = append(chunks, anthropic.RetrievedChunk{
			Source:     p.Source,
			Content:    p.Content,
			Similarity: p.Similarity,
		})
	}

	for _, c := range retrieved.CodeExamples {
		chunks = append(chunks, anthropic.RetrievedChunk{
			Source:     c.Source,
			Content:    c.Content,
			Similarity: c.Similarity,
		})
	}

	return chunks, nil
}

// Close closes the RAG retriever resources.
func (r *RAGRetriever) Close() error {
	if r.store != nil {
		return r.store.Close()
	}
	return nil
}
