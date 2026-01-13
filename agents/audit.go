// Package agents provides agent spawning and management for the AI development factory.
package agents

import (
	"context"
	"encoding/json"
	"time"

	"factory/kanban"
)

// AuditLogger provides audit logging for agent operations.
type AuditLogger interface {
	// LogPromptSent records the prompt sent to an agent.
	LogPromptSent(runID, ticketID, agent, prompt string) error

	// LogResponseReceived records the response from an agent.
	LogResponseReceived(runID, ticketID, agent, response string, tokenIn, tokenOut int, durationMs int) error

	// LogToolCall records a tool call made by an agent (API mode only).
	LogToolCall(runID, ticketID, agent, tool, args string) error

	// LogError records an error during agent execution.
	LogError(runID, ticketID, agent, errorMsg string) error
}

// StoreAuditLogger implements AuditLogger using a StateStore.
type StoreAuditLogger struct {
	store    AuditStore
	enabled  bool
}

// AuditStore is the interface that the store must implement for audit logging.
type AuditStore interface {
	AddAuditEntry(entry *kanban.AuditEntry) error
	GetConfigValue(key string) (string, error)
}

// NewStoreAuditLogger creates a new store-backed audit logger.
func NewStoreAuditLogger(store AuditStore) *StoreAuditLogger {
	enabled := true
	if v, _ := store.GetConfigValue("enable_audit_logging"); v == "false" {
		enabled = false
	}

	return &StoreAuditLogger{
		store:   store,
		enabled: enabled,
	}
}

// generateID creates a unique ID for an audit entry.
func generateID() string {
	return time.Now().Format("20060102-150405.000000")
}

// LogPromptSent records the prompt sent to an agent.
func (l *StoreAuditLogger) LogPromptSent(runID, ticketID, agent, prompt string) error {
	if !l.enabled {
		return nil
	}

	// Truncate very long prompts for storage efficiency (keep first 50KB)
	eventData := prompt
	if len(eventData) > 50000 {
		eventData = eventData[:50000] + "\n...[truncated]"
	}

	entry := &kanban.AuditEntry{
		ID:        generateID(),
		RunID:     runID,
		TicketID:  ticketID,
		Agent:     agent,
		EventType: kanban.AuditEventPromptSent,
		EventData: eventData,
		CreatedAt: time.Now(),
	}

	return l.store.AddAuditEntry(entry)
}

// LogResponseReceived records the response from an agent.
func (l *StoreAuditLogger) LogResponseReceived(runID, ticketID, agent, response string, tokenIn, tokenOut int, durationMs int) error {
	if !l.enabled {
		return nil
	}

	// Create structured event data
	data := map[string]interface{}{
		"response":    response,
		"token_input": tokenIn,
		"token_output": tokenOut,
		"duration_ms": durationMs,
	}

	// Truncate response if needed
	if len(response) > 50000 {
		data["response"] = response[:50000] + "\n...[truncated]"
		data["truncated"] = true
		data["original_length"] = len(response)
	}

	eventDataJSON, _ := json.Marshal(data)

	entry := &kanban.AuditEntry{
		ID:          generateID(),
		RunID:       runID,
		TicketID:    ticketID,
		Agent:       agent,
		EventType:   kanban.AuditEventResponseReceived,
		EventData:   string(eventDataJSON),
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		DurationMs:  durationMs,
		CreatedAt:   time.Now(),
	}

	return l.store.AddAuditEntry(entry)
}

// LogToolCall records a tool call made by an agent.
func (l *StoreAuditLogger) LogToolCall(runID, ticketID, agent, tool, args string) error {
	if !l.enabled {
		return nil
	}

	data := map[string]interface{}{
		"tool": tool,
		"args": args,
	}
	eventDataJSON, _ := json.Marshal(data)

	entry := &kanban.AuditEntry{
		ID:        generateID(),
		RunID:     runID,
		TicketID:  ticketID,
		Agent:     agent,
		EventType: kanban.AuditEventToolCall,
		EventData: string(eventDataJSON),
		CreatedAt: time.Now(),
	}

	return l.store.AddAuditEntry(entry)
}

// LogError records an error during agent execution.
func (l *StoreAuditLogger) LogError(runID, ticketID, agent, errorMsg string) error {
	if !l.enabled {
		return nil
	}

	entry := &kanban.AuditEntry{
		ID:        generateID(),
		RunID:     runID,
		TicketID:  ticketID,
		Agent:     agent,
		EventType: kanban.AuditEventError,
		EventData: errorMsg,
		CreatedAt: time.Now(),
	}

	return l.store.AddAuditEntry(entry)
}

// NoOpAuditLogger is an audit logger that does nothing (for when logging is disabled).
type NoOpAuditLogger struct{}

func (l *NoOpAuditLogger) LogPromptSent(runID, ticketID, agent, prompt string) error { return nil }
func (l *NoOpAuditLogger) LogResponseReceived(runID, ticketID, agent, response string, tokenIn, tokenOut int, durationMs int) error { return nil }
func (l *NoOpAuditLogger) LogToolCall(runID, ticketID, agent, tool, args string) error { return nil }
func (l *NoOpAuditLogger) LogError(runID, ticketID, agent, errorMsg string) error { return nil }

// AuditingSpawner wraps an AgentSpawner to add audit logging.
type AuditingSpawner struct {
	inner  AgentSpawner
	logger AuditLogger
}

// NewAuditingSpawner creates a spawner wrapper that logs all agent interactions.
func NewAuditingSpawner(inner AgentSpawner, logger AuditLogger) *AuditingSpawner {
	return &AuditingSpawner{
		inner:  inner,
		logger: logger,
	}
}

// SpawnAgent runs an agent and logs the interaction.
func (s *AuditingSpawner) SpawnAgent(ctx context.Context, agentType AgentType, data PromptData, workDir string) (*AgentResult, error) {
	startTime := time.Now()

	// Generate run ID for correlation
	runID := ""
	ticketID := ""
	if data.Ticket != nil {
		ticketID = data.Ticket.ID
		runID = ticketID + "-" + string(agentType) + "-" + startTime.Format("20060102-150405")
	} else {
		runID = string(agentType) + "-" + startTime.Format("20060102-150405")
	}

	// Log the prompt (we'll render it ourselves for logging)
	// Note: The actual prompt rendering happens inside the spawner, so we log the data we have
	promptSummary := formatPromptSummary(agentType, data)
	if err := s.logger.LogPromptSent(runID, ticketID, string(agentType), promptSummary); err != nil {
		// Non-fatal - continue with agent execution
	}

	// Run the actual agent
	result, err := s.inner.SpawnAgent(ctx, agentType, data, workDir)

	durationMs := int(time.Since(startTime).Milliseconds())

	if err != nil {
		// Log the error
		s.logger.LogError(runID, ticketID, string(agentType), err.Error())
		return result, err
	}

	// Log the response
	// Token counts aren't available for CLI mode, but will be for API mode
	tokenIn, tokenOut := 0, 0
	if result != nil {
		s.logger.LogResponseReceived(runID, ticketID, string(agentType), result.Output, tokenIn, tokenOut, durationMs)

		if !result.Success && result.Error != "" {
			s.logger.LogError(runID, ticketID, string(agentType), result.Error)
		}
	}

	return result, err
}

// ValidateAgentEnvironment delegates to the inner spawner.
func (s *AuditingSpawner) ValidateAgentEnvironment() []string {
	return s.inner.ValidateAgentEnvironment()
}

// formatPromptSummary creates a summary of the prompt data for logging.
func formatPromptSummary(agentType AgentType, data PromptData) string {
	summary := map[string]interface{}{
		"agent_type":    string(agentType),
		"worktree_path": data.WorktreePath,
		"domain":        data.Domain,
	}

	if data.Ticket != nil {
		summary["ticket_id"] = data.Ticket.ID
		summary["ticket_title"] = data.Ticket.Title
		summary["ticket_status"] = string(data.Ticket.Status)
	}

	if data.RawIdea != "" {
		summary["raw_idea"] = truncateForSummary(data.RawIdea, 500)
	}

	if len(data.Questions) > 0 {
		summary["questions"] = data.Questions
	}

	if data.CurrentRound > 0 {
		summary["current_round"] = data.CurrentRound
		summary["agent"] = data.Agent
	}

	if data.ExtraContext != "" {
		summary["extra_context"] = truncateForSummary(data.ExtraContext, 500)
	}

	jsonBytes, _ := json.MarshalIndent(summary, "", "  ")
	return string(jsonBytes)
}

// truncateForSummary shortens a string for summary logging.
func truncateForSummary(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
