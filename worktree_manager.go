// Package factory implements the AI development factory orchestrator.
package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"factory/kanban"
)

// WorktreeStore extends StateStore for worktree management.
// This interface defines the methods needed for worktree pool management,
// merge queue operations, and event logging.
type WorktreeStore interface {
	// Pool management
	GetWorktreePool() ([]kanban.WorktreePoolEntry, error)
	GetActiveWorktreeCount() (int, error)
	RegisterWorktree(entry kanban.WorktreePoolEntry) error
	UpdateWorktreeStatus(ticketID string, status kanban.WorktreePoolStatus) error
	RemoveFromPool(ticketID string) error
	GetWorktreePoolStats() (*kanban.WorktreePoolStats, error)

	// Merge queue
	QueueMerge(entry kanban.MergeQueueEntry) error
	GetPendingMerges() ([]kanban.MergeQueueEntry, error)
	GetMergeByTicket(ticketID string) (*kanban.MergeQueueEntry, error)
	UpdateMergeStatus(id string, status kanban.MergeQueueStatus, lastError string) error
	CompleteMerge(id string) error
	FailMerge(id string, err string) error

	// Events
	LogWorktreeEvent(event kanban.WorktreeEvent) error
	GetWorktreeEvents(ticketID string) ([]kanban.WorktreeEvent, error)

	// Config
	GetConfigValue(key string) (string, error)
	SetConfigValue(key, value string) error

	// Existing state store methods we need (matching kanban.StateStore signatures)
	GetTicketsByStatus(status kanban.Status) []kanban.Ticket
	GetTicket(id string) (*kanban.Ticket, bool)
	UpdateTicketStatus(id string, newStatus kanban.Status, by string, note string) error
	GetActiveRunsForTicket(ticketID string) []kanban.AgentRun

	// Conversation methods for notifications
	CreateConversation(conv *kanban.TicketConversation) error
	AddConversationMessage(msg *kanban.ConversationMessage) error
}

// WorktreeManagerConfig holds configuration for the worktree manager.
type WorktreeManagerConfig struct {
	MaxGlobalWorktrees     int           // Maximum concurrent worktrees (default: 3)
	MergeAfterDevSignoff   bool          // Merge to main after dev completes (default: true)
	CleanupWorktreeOnMerge bool          // Remove worktree after merge (default: false)
	CheckInterval          time.Duration // How often to check (default: 30s)
	MaxMergeAttempts       int           // Max retry attempts for merge (default: 3)
}

// DefaultWorktreeManagerConfig returns sensible defaults.
func DefaultWorktreeManagerConfig() WorktreeManagerConfig {
	return WorktreeManagerConfig{
		MaxGlobalWorktrees:     3,
		MergeAfterDevSignoff:   true,
		CleanupWorktreeOnMerge: false,
		CheckInterval:          30 * time.Second,
		MaxMergeAttempts:       3,
	}
}

// runWorktreeBackground is the Worktree Manager agent's background work loop.
// It manages the global worktree pool, processes the merge queue, and coordinates
// with DEV, QA, and PM agents for worktree lifecycle management.
func (m *BackgroundAgentManager) runWorktreeBackground(ctx context.Context) error {
	m.updateAgentStatus(m.agents[BackgroundWorktree], "Running", "Managing worktree pool")

	// Check if the state supports worktree management
	worktreeStore, ok := m.orchestrator.state.(WorktreeStore)
	if !ok {
		m.orchestrator.logger.Debug("State store does not support worktree management")
		return nil
	}

	// Get configuration
	config := m.getWorktreeConfig(worktreeStore)

	// 1. Process the merge queue - handle pending merges
	m.updateAgentStatus(m.agents[BackgroundWorktree], "Running", "Processing merge queue")
	if err := m.processMergeQueue(ctx, worktreeStore, config); err != nil {
		m.orchestrator.logger.Error("Error processing merge queue", "error", err)
	}

	// 2. Detect dev signoffs - find tickets transitioning to IN_QA
	m.updateAgentStatus(m.agents[BackgroundWorktree], "Running", "Detecting dev signoffs")
	if err := m.detectDevSignoffs(ctx, worktreeStore, config); err != nil {
		m.orchestrator.logger.Error("Error detecting dev signoffs", "error", err)
	}

	// 3. Enforce worktree limits - check if new dev work can start
	m.updateAgentStatus(m.agents[BackgroundWorktree], "Running", "Checking worktree limits")
	if err := m.enforceWorktreeLimits(ctx, worktreeStore, config); err != nil {
		m.orchestrator.logger.Error("Error enforcing worktree limits", "error", err)
	}

	// 4. Cleanup completed worktrees - remove merged/done worktrees
	m.updateAgentStatus(m.agents[BackgroundWorktree], "Running", "Cleaning up completed worktrees")
	if err := m.cleanupCompletedWorktrees(ctx, worktreeStore, config); err != nil {
		m.orchestrator.logger.Error("Error cleaning up worktrees", "error", err)
	}

	// Log pool stats
	stats, err := worktreeStore.GetWorktreePoolStats()
	if err == nil {
		m.orchestrator.logger.Info("Worktree pool status",
			"active", stats.ActiveCount,
			"merging", stats.MergingCount,
			"limit", stats.Limit,
			"available", stats.AvailableSlots)
	}

	return nil
}

// getWorktreeConfig loads worktree configuration from the database.
func (m *BackgroundAgentManager) getWorktreeConfig(store WorktreeStore) WorktreeManagerConfig {
	config := DefaultWorktreeManagerConfig()

	// Load from database config
	if val, err := store.GetConfigValue("max_global_worktrees"); err == nil && val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			config.MaxGlobalWorktrees = n
		}
	}

	if val, err := store.GetConfigValue("merge_after_dev_signoff"); err == nil && val != "" {
		config.MergeAfterDevSignoff = val == "true"
	}

	if val, err := store.GetConfigValue("cleanup_worktree_on_merge"); err == nil && val != "" {
		config.CleanupWorktreeOnMerge = val == "true"
	}

	if val, err := store.GetConfigValue("worktree_check_interval"); err == nil && val != "" {
		if seconds, err := strconv.Atoi(val); err == nil {
			config.CheckInterval = time.Duration(seconds) * time.Second
		}
	}

	return config
}

// processMergeQueue handles pending merge operations with retry logic.
// This is the core merge processing function that:
// 1. Gets pending merges from the queue
// 2. Attempts squash merge to main
// 3. Pushes to remote
// 4. Notifies QA on success
// 5. Handles failures with retry or escalation.
func (m *BackgroundAgentManager) processMergeQueue(ctx context.Context, store WorktreeStore, config WorktreeManagerConfig) error {
	pendingMerges, err := store.GetPendingMerges()
	if err != nil {
		return fmt.Errorf("failed to get pending merges: %w", err)
	}

	for _, merge := range pendingMerges {
		// Update status to in_progress
		if err := store.UpdateMergeStatus(merge.ID, kanban.MergeQueueStatusInProgress, ""); err != nil {
			m.orchestrator.logger.Error("Failed to update merge status", "id", merge.ID, "error", err)
			continue
		}

		// Log merge start event
		_ = store.LogWorktreeEvent(kanban.WorktreeEvent{
			ID:        fmt.Sprintf("evt-%s-%d", merge.TicketID, time.Now().UnixNano()),
			TicketID:  merge.TicketID,
			EventType: kanban.WorktreeEventMergeStarted,
			EventData: fmt.Sprintf(`{"branch":"%s","attempt":%d}`, merge.Branch, merge.Attempts+1),
			CreatedAt: time.Now(),
		})

		// Attempt the merge
		mergeErr := m.performMerge(ctx, store, &merge)

		if mergeErr != nil {
			// Increment attempts
			newAttempts := merge.Attempts + 1

			if newAttempts >= config.MaxMergeAttempts {
				// Max attempts reached - fail permanently
				m.orchestrator.logger.Error("Merge failed after max attempts",
					"ticket", merge.TicketID,
					"branch", merge.Branch,
					"attempts", newAttempts,
					"error", mergeErr)

				if err := store.FailMerge(merge.ID, mergeErr.Error()); err != nil {
					m.orchestrator.logger.Error("Failed to mark merge as failed", "id", merge.ID, "error", err)
				}

				// Log failure event
				_ = store.LogWorktreeEvent(kanban.WorktreeEvent{
					ID:        fmt.Sprintf("evt-%s-%d", merge.TicketID, time.Now().UnixNano()),
					TicketID:  merge.TicketID,
					EventType: kanban.WorktreeEventMergeFailed,
					EventData: fmt.Sprintf(`{"branch":"%s","error":"%s","attempts":%d}`, merge.Branch, mergeErr.Error(), newAttempts),
					CreatedAt: time.Now(),
				})

				// Create escalation conversation
				m.createMergeFailureConversation(store, merge.TicketID, mergeErr.Error())

				// Update ticket status to BLOCKED
				_ = store.UpdateTicketStatus(merge.TicketID, kanban.StatusBlocked, "WorktreeManager", "Merge to main failed after multiple attempts")

				// Broadcast SSE event
				if m.orchestrator.state != nil {
					if broadcaster, ok := m.orchestrator.state.(interface{ Broadcast(string) }); ok {
						broadcaster.Broadcast(fmt.Sprintf("merge-failed:%s", merge.TicketID))
					}
				}
			} else {
				// Retry later
				m.orchestrator.logger.Warn("Merge attempt failed, will retry",
					"ticket", merge.TicketID,
					"attempt", newAttempts,
					"error", mergeErr)

				if err := store.UpdateMergeStatus(merge.ID, kanban.MergeQueueStatusPending, mergeErr.Error()); err != nil {
					m.orchestrator.logger.Error("Failed to update merge status for retry", "id", merge.ID, "error", err)
				}
			}
		} else {
			// Merge succeeded
			m.orchestrator.logger.Info("Merge completed successfully",
				"ticket", merge.TicketID,
				"branch", merge.Branch)

			if err := store.CompleteMerge(merge.ID); err != nil {
				m.orchestrator.logger.Error("Failed to mark merge as complete", "id", merge.ID, "error", err)
			}

			// Update worktree pool status
			_ = store.UpdateWorktreeStatus(merge.TicketID, kanban.WorktreePoolStatusMerging)

			// Log success event
			_ = store.LogWorktreeEvent(kanban.WorktreeEvent{
				ID:        fmt.Sprintf("evt-%s-%d", merge.TicketID, time.Now().UnixNano()),
				TicketID:  merge.TicketID,
				EventType: kanban.WorktreeEventMergeCompleted,
				EventData: fmt.Sprintf(`{"branch":"%s"}`, merge.Branch),
				CreatedAt: time.Now(),
			})

			// Notify QA that main has been updated
			m.notifyQAMainUpdated(store, merge.TicketID)

			// Broadcast SSE event
			if m.orchestrator.state != nil {
				if broadcaster, ok := m.orchestrator.state.(interface{ Broadcast(string) }); ok {
					broadcaster.Broadcast(fmt.Sprintf("merge-complete:%s", merge.TicketID))
				}
			}
		}
	}

	return nil
}

// performMerge executes the actual git merge operation.
func (m *BackgroundAgentManager) performMerge(ctx context.Context, store WorktreeStore, merge *kanban.MergeQueueEntry) error {
	// Get ticket for commit message
	ticket, found := store.GetTicket(merge.TicketID)
	if !found {
		return fmt.Errorf("ticket not found: %s", merge.TicketID)
	}

	// Build commit message
	commitMsg := fmt.Sprintf("feat(%s): %s\n\nTicket: %s\nMerged-by: WorktreeManager",
		ticket.Domain, ticket.Title, ticket.ID)

	// Perform squash merge using the orchestrator's worktree manager
	if err := m.orchestrator.worktree.SquashMerge(merge.Branch, commitMsg); err != nil {
		return fmt.Errorf("squash merge failed: %w", err)
	}

	// Push to main
	if err := m.orchestrator.worktree.PushMain(); err != nil {
		return fmt.Errorf("push to main failed: %w", err)
	}

	// Update ticket's worktree merged status
	if ticket.Worktree != nil {
		ticket.Worktree.Merged = true
	}

	return nil
}

// detectDevSignoffs finds tickets that have completed dev and should be merged.
// This is triggered when a ticket transitions from IN_DEV to IN_QA with dev signoff.
func (m *BackgroundAgentManager) detectDevSignoffs(ctx context.Context, store WorktreeStore, config WorktreeManagerConfig) error {
	if !config.MergeAfterDevSignoff {
		return nil
	}

	// Get tickets in QA that have dev signoff but haven't been merged
	qaTickets := store.GetTicketsByStatus(kanban.StatusInQA)

	for _, ticket := range qaTickets {
		// Check if dev has signed off (Signoffs.Dev is a bool field)
		if !ticket.Signoffs.Dev {
			continue
		}

		// Check if worktree exists and hasn't been merged
		if ticket.Worktree == nil || ticket.Worktree.Merged {
			continue
		}

		// Check if already in merge queue
		existingMerge, _ := store.GetMergeByTicket(ticket.ID)
		if existingMerge != nil {
			continue // Already queued
		}

		// Queue the merge
		mergeEntry := kanban.MergeQueueEntry{
			ID:        fmt.Sprintf("merge-%s-%d", ticket.ID, time.Now().Unix()),
			TicketID:  ticket.ID,
			Branch:    ticket.Worktree.Branch,
			Status:    kanban.MergeQueueStatusPending,
			Attempts:  0,
			CreatedAt: time.Now(),
		}

		if err := store.QueueMerge(mergeEntry); err != nil {
			m.orchestrator.logger.Error("Failed to queue merge", "ticket", ticket.ID, "error", err)
			continue
		}

		m.orchestrator.logger.Info("Queued merge for ticket with dev signoff",
			"ticket", ticket.ID,
			"branch", ticket.Worktree.Branch)

		// Log event
		_ = store.LogWorktreeEvent(kanban.WorktreeEvent{
			ID:        fmt.Sprintf("evt-%s-%d", ticket.ID, time.Now().UnixNano()),
			TicketID:  ticket.ID,
			EventType: kanban.WorktreeEventMergeStarted,
			EventData: fmt.Sprintf(`{"reason":"dev_signoff","branch":"%s"}`, ticket.Worktree.Branch),
			CreatedAt: time.Now(),
		})

		// Broadcast SSE event
		if m.orchestrator.state != nil {
			if broadcaster, ok := m.orchestrator.state.(interface{ Broadcast(string) }); ok {
				broadcaster.Broadcast(fmt.Sprintf("merge-queued:%s", ticket.ID))
			}
		}
	}

	return nil
}

// enforceWorktreeLimits checks the global worktree limit and logs status.
// When the limit is reached, new dev work cannot start until a slot opens.
func (m *BackgroundAgentManager) enforceWorktreeLimits(ctx context.Context, store WorktreeStore, config WorktreeManagerConfig) error {
	activeCount, err := store.GetActiveWorktreeCount()
	if err != nil {
		return fmt.Errorf("failed to get active worktree count: %w", err)
	}

	if activeCount >= config.MaxGlobalWorktrees {
		// Check if there are tickets waiting
		readyTickets := store.GetTicketsByStatus(kanban.StatusReady)
		if len(readyTickets) > 0 {
			m.orchestrator.logger.Info("Worktree limit reached, tickets waiting",
				"active", activeCount,
				"limit", config.MaxGlobalWorktrees,
				"waiting", len(readyTickets))

			// Log limit enforcement event for first waiting ticket
			_ = store.LogWorktreeEvent(kanban.WorktreeEvent{
				ID:        fmt.Sprintf("evt-limit-%d", time.Now().UnixNano()),
				TicketID:  readyTickets[0].ID,
				EventType: kanban.WorktreeEventLimitEnforced,
				EventData: fmt.Sprintf(`{"active":%d,"limit":%d,"waiting":%d}`, activeCount, config.MaxGlobalWorktrees, len(readyTickets)),
				CreatedAt: time.Now(),
			})

			// Broadcast SSE event
			if m.orchestrator.state != nil {
				if broadcaster, ok := m.orchestrator.state.(interface{ Broadcast(string) }); ok {
					broadcaster.Broadcast("worktree-limit-reached")
				}
			}
		}
	}

	return nil
}

// cleanupCompletedWorktrees removes worktrees for DONE tickets if configured.
func (m *BackgroundAgentManager) cleanupCompletedWorktrees(ctx context.Context, store WorktreeStore, config WorktreeManagerConfig) error {
	if !config.CleanupWorktreeOnMerge {
		return nil
	}

	// Get pool entries in cleanup_pending status
	pool, err := store.GetWorktreePool()
	if err != nil {
		return fmt.Errorf("failed to get worktree pool: %w", err)
	}

	for _, entry := range pool {
		if entry.Status != kanban.WorktreePoolStatusCleanupPending {
			continue
		}

		// Get ticket to check if DONE
		ticket, found := store.GetTicket(entry.TicketID)
		if !found {
			// Ticket not found, remove from pool
			_ = store.RemoveFromPool(entry.TicketID)
			continue
		}

		if ticket.Status != kanban.StatusDone {
			continue // Not ready for cleanup
		}

		// Remove the worktree
		if err := m.orchestrator.worktree.RemoveWorktree(entry.Path, true); err != nil {
			m.orchestrator.logger.Warn("Failed to remove worktree",
				"ticket", entry.TicketID,
				"path", entry.Path,
				"error", err)
			continue
		}

		// Remove from pool
		if err := store.RemoveFromPool(entry.TicketID); err != nil {
			m.orchestrator.logger.Error("Failed to remove from pool", "ticket", entry.TicketID, "error", err)
			continue
		}

		// Log cleanup event
		_ = store.LogWorktreeEvent(kanban.WorktreeEvent{
			ID:        fmt.Sprintf("evt-%s-%d", entry.TicketID, time.Now().UnixNano()),
			TicketID:  entry.TicketID,
			EventType: kanban.WorktreeEventCleanedUp,
			EventData: fmt.Sprintf(`{"path":"%s","branch":"%s"}`, entry.Path, entry.Branch),
			CreatedAt: time.Now(),
		})

		m.orchestrator.logger.Info("Cleaned up worktree",
			"ticket", entry.TicketID,
			"path", entry.Path)
	}

	return nil
}

// notifyQAMainUpdated creates a conversation notifying QA that main has been updated.
func (m *BackgroundAgentManager) notifyQAMainUpdated(store WorktreeStore, ticketID string) {
	convID := fmt.Sprintf("conv-merge-%s-%d", ticketID, time.Now().Unix())

	// Create the conversation thread
	conv := &kanban.TicketConversation{
		ID:         convID,
		TicketID:   ticketID,
		ThreadType: kanban.ThreadTypeQAFeedback,
		Title:      "Code merged to main - ready for QA",
		Status:     kanban.ThreadStatusOpen,
		CreatedAt:  time.Now(),
	}

	if err := store.CreateConversation(conv); err != nil {
		m.orchestrator.logger.Error("Failed to create merge notification conversation",
			"ticket", ticketID,
			"error", err)
		return
	}

	// Add the notification message
	metadata := map[string]interface{}{
		"event_type": "merge_complete",
		"source":     "worktree_manager",
	}
	metadataJSON, _ := json.Marshal(metadata)

	msg := &kanban.ConversationMessage{
		ID:             fmt.Sprintf("msg-%s-1", convID),
		ConversationID: convID,
		Agent:          "WorktreeManager",
		MessageType:    kanban.MessageTypeStatusUpdate,
		Content:        "Feature branch has been squash-merged to main. QA can now test from the main branch. All development changes are integrated and ready for testing.",
		Metadata:       string(metadataJSON),
		CreatedAt:      time.Now(),
	}

	if err := store.AddConversationMessage(msg); err != nil {
		m.orchestrator.logger.Error("Failed to add merge notification message",
			"conversation", convID,
			"error", err)
	}
}

// createMergeFailureConversation creates an escalation conversation when merge fails.
func (m *BackgroundAgentManager) createMergeFailureConversation(store WorktreeStore, ticketID, lastError string) {
	convID := fmt.Sprintf("conv-merge-fail-%s-%d", ticketID, time.Now().Unix())

	// Create the conversation thread (escalated status)
	conv := &kanban.TicketConversation{
		ID:         convID,
		TicketID:   ticketID,
		ThreadType: kanban.ThreadTypeBlocker,
		Title:      "Merge to main failed - manual intervention required",
		Status:     kanban.ThreadStatusEscalated,
		CreatedAt:  time.Now(),
	}

	if err := store.CreateConversation(conv); err != nil {
		m.orchestrator.logger.Error("Failed to create merge failure conversation",
			"ticket", ticketID,
			"error", err)
		return
	}

	// Add the failure message
	metadata := map[string]interface{}{
		"event_type": "merge_failed",
		"source":     "worktree_manager",
		"last_error": lastError,
	}
	metadataJSON, _ := json.Marshal(metadata)

	msg := &kanban.ConversationMessage{
		ID:             fmt.Sprintf("msg-%s-1", convID),
		ConversationID: convID,
		Agent:          "WorktreeManager",
		MessageType:    kanban.MessageTypeQuestion,
		Content: fmt.Sprintf(`Failed to merge feature branch to main after 3 attempts.

**Error:** %s

**Required Action:**
1. Review the merge conflicts or issues manually
2. Resolve any conflicts in the feature branch
3. Manually merge to main or re-queue the merge

This ticket has been marked as BLOCKED until the merge issue is resolved.`, lastError),
		Metadata:  string(metadataJSON),
		CreatedAt: time.Now(),
	}

	if err := store.AddConversationMessage(msg); err != nil {
		m.orchestrator.logger.Error("Failed to add merge failure message",
			"conversation", convID,
			"error", err)
	}
}

// CanStartDevWork checks if a new dev agent can start based on worktree limits.
// This is called by the orchestrator before spawning a dev agent.
func (m *BackgroundAgentManager) CanStartDevWork() bool {
	worktreeStore, ok := m.orchestrator.state.(WorktreeStore)
	if !ok {
		return true // No limit enforcement if store doesn't support it
	}

	config := m.getWorktreeConfig(worktreeStore)
	activeCount, err := worktreeStore.GetActiveWorktreeCount()
	if err != nil {
		m.orchestrator.logger.Warn("Failed to check worktree count, allowing dev work", "error", err)
		return true
	}

	return activeCount < config.MaxGlobalWorktrees
}

// RegisterDevWorktree registers a new dev worktree in the global pool.
// This is called by the orchestrator after creating a worktree for a dev agent.
func (m *BackgroundAgentManager) RegisterDevWorktree(ticketID, branch, path, agent string) error {
	worktreeStore, ok := m.orchestrator.state.(WorktreeStore)
	if !ok {
		return nil // No registration if store doesn't support it
	}

	entry := kanban.WorktreePoolEntry{
		ID:           fmt.Sprintf("wt-%s-%d", ticketID, time.Now().Unix()),
		TicketID:     ticketID,
		Branch:       branch,
		Path:         path,
		Agent:        agent,
		Status:       kanban.WorktreePoolStatusActive,
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
	}

	if err := worktreeStore.RegisterWorktree(entry); err != nil {
		return fmt.Errorf("failed to register worktree: %w", err)
	}

	// Log creation event
	_ = worktreeStore.LogWorktreeEvent(kanban.WorktreeEvent{
		ID:        fmt.Sprintf("evt-%s-%d", ticketID, time.Now().UnixNano()),
		TicketID:  ticketID,
		EventType: kanban.WorktreeEventCreated,
		EventData: fmt.Sprintf(`{"branch":"%s","path":"%s","agent":"%s"}`, branch, path, agent),
		CreatedAt: time.Now(),
	})

	// Broadcast SSE event
	if m.orchestrator.state != nil {
		if broadcaster, ok := m.orchestrator.state.(interface{ Broadcast(string) }); ok {
			broadcaster.Broadcast("worktree-pool-update")
		}
	}

	return nil
}

// GetWorktreePoolStats returns the current worktree pool statistics.
func (m *BackgroundAgentManager) GetWorktreePoolStats() (*kanban.WorktreePoolStats, error) {
	worktreeStore, ok := m.orchestrator.state.(WorktreeStore)
	if !ok {
		return nil, fmt.Errorf("state store does not support worktree management")
	}

	return worktreeStore.GetWorktreePoolStats()
}
