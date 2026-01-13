package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"factory/kanban"
)

// BackgroundAgentType represents a type of always-running background agent.
type BackgroundAgentType string

const (
	BackgroundPM       BackgroundAgentType = "PM"
	BackgroundSecurity BackgroundAgentType = "Security"
	BackgroundGatherer BackgroundAgentType = "Gatherer"
	BackgroundWorktree BackgroundAgentType = "Worktree"
)

// BackgroundAgentStatus represents the current state of a background agent.
type BackgroundAgentStatus struct {
	Type            BackgroundAgentType `json:"type"`
	Status          string              `json:"status"` // "Running", "Idle", "Paused"
	CurrentActivity string              `json:"currentActivity"`
	LastActiveAt    time.Time           `json:"lastActiveAt"`
	CycleCount      int                 `json:"cycleCount"` // How many work cycles completed
}

// BackgroundAgentManager manages always-running background agents.
type BackgroundAgentManager struct {
	orchestrator *Orchestrator
	agents       map[BackgroundAgentType]*backgroundAgent
	mu           sync.RWMutex
	stopCh       chan struct{}
}

type backgroundAgent struct {
	agentType       BackgroundAgentType
	status          BackgroundAgentStatus
	interval        time.Duration // How often the agent runs its cycle
	runFunc         func(context.Context) error
	mu              sync.RWMutex
}

// NewBackgroundAgentManager creates a new background agent manager.
func NewBackgroundAgentManager(o *Orchestrator) *BackgroundAgentManager {
	m := &BackgroundAgentManager{
		orchestrator: o,
		agents:       make(map[BackgroundAgentType]*backgroundAgent),
		stopCh:       make(chan struct{}),
	}

	// Register background agents
	m.registerAgent(BackgroundPM, 30*time.Second, m.runPMBackground)
	m.registerAgent(BackgroundSecurity, 2*time.Minute, m.runSecurityBackground)
	m.registerAgent(BackgroundGatherer, 5*time.Minute, m.runGathererBackground)
	m.registerAgent(BackgroundWorktree, 30*time.Second, m.runWorktreeBackground)

	return m
}

func (m *BackgroundAgentManager) registerAgent(agentType BackgroundAgentType, interval time.Duration, runFunc func(context.Context) error) {
	m.agents[agentType] = &backgroundAgent{
		agentType: agentType,
		status: BackgroundAgentStatus{
			Type:            agentType,
			Status:          "Idle",
			CurrentActivity: "Waiting to start",
			LastActiveAt:    time.Now(),
		},
		interval: interval,
		runFunc:  runFunc,
	}
}

// Start starts all background agents.
func (m *BackgroundAgentManager) Start(ctx context.Context) {
	m.orchestrator.logger.Info("Starting background agents")

	for _, agent := range m.agents {
		go m.runAgentLoop(ctx, agent)
	}
}

// Stop stops all background agents.
func (m *BackgroundAgentManager) Stop() {
	close(m.stopCh)
}

// GetStatuses returns the current status of all background agents.
func (m *BackgroundAgentManager) GetStatuses() []BackgroundAgentStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make([]BackgroundAgentStatus, 0, len(m.agents))
	for _, agent := range m.agents {
		agent.mu.RLock()
		statuses = append(statuses, agent.status)
		agent.mu.RUnlock()
	}
	return statuses
}

func (m *BackgroundAgentManager) updateAgentStatus(agent *backgroundAgent, status, activity string) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	agent.status.Status = status
	agent.status.CurrentActivity = activity
	agent.status.LastActiveAt = time.Now()
}

func (m *BackgroundAgentManager) runAgentLoop(ctx context.Context, agent *backgroundAgent) {
	ticker := time.NewTicker(agent.interval)
	defer ticker.Stop()

	// Run immediately on start
	m.executeAgentCycle(ctx, agent)

	for {
		select {
		case <-ctx.Done():
			m.updateAgentStatus(agent, "Stopped", "Context cancelled")
			return
		case <-m.stopCh:
			m.updateAgentStatus(agent, "Stopped", "Shutdown requested")
			return
		case <-ticker.C:
			m.executeAgentCycle(ctx, agent)
		}
	}
}

func (m *BackgroundAgentManager) executeAgentCycle(ctx context.Context, agent *backgroundAgent) {
	m.updateAgentStatus(agent, "Running", "Starting cycle")

	if err := agent.runFunc(ctx); err != nil {
		m.orchestrator.logger.Error("Background agent cycle failed",
			"agent", agent.agentType,
			"error", err)
		m.updateAgentStatus(agent, "Error", err.Error())
		return
	}

	agent.mu.Lock()
	agent.status.CycleCount++
	agent.mu.Unlock()

	m.updateAgentStatus(agent, "Idle", "Waiting for next cycle")
}

// --- Background Agent Implementations ---

// runPMBackground is the PM agent's background work loop.
// It monitors the pipeline and manages work flow.
func (m *BackgroundAgentManager) runPMBackground(ctx context.Context) error {
	m.updateAgentStatus(m.agents[BackgroundPM], "Running", "Monitoring pipeline")

	// PM monitors several things:
	// 1. Check for blocked tickets that might be unblocked
	// 2. Review ticket priorities
	// 3. Monitor iteration progress
	// 4. Look for stalled work
	// 5. Self-heal stuck tickets (IN_DEV with no active agent)
	// 6. Perform periodic check-ins on active development tickets

	state := m.orchestrator.state
	stats := state.GetStats()

	// Check for stalled tickets (in progress for too long)
	activeRuns := state.GetActiveRuns()
	for _, run := range activeRuns {
		if time.Since(run.StartedAt) > 30*time.Minute {
			m.orchestrator.logger.Warn("PM: Detected potentially stalled agent",
				"agent", run.Agent,
				"ticket", run.TicketID,
				"duration", time.Since(run.StartedAt))
			m.updateAgentStatus(m.agents[BackgroundPM], "Running",
				"Reviewing stalled work: "+run.TicketID)
		}
	}

	// Self-heal: Check for stuck IN_DEV tickets with no active running agent
	m.healStuckDevTickets(state, activeRuns)

	// Perform PM check-ins on IN_DEV tickets
	m.performPMCheckins(ctx, state)

	// Check iteration progress
	if stats[kanban.StatusDone] > 0 {
		total := stats[kanban.StatusReady] + stats[kanban.StatusInDev] +
			stats[kanban.StatusInQA] + stats[kanban.StatusInUX] +
			stats[kanban.StatusInSec] + stats[kanban.StatusPMReview] +
			stats[kanban.StatusDone]
		progress := float64(stats[kanban.StatusDone]) / float64(total) * 100
		m.updateAgentStatus(m.agents[BackgroundPM], "Running",
			"Iteration progress: "+string(rune(int(progress)))+"% complete")
	}

	// Check for blocked tickets
	if stats[kanban.StatusBlocked] > 0 {
		m.updateAgentStatus(m.agents[BackgroundPM], "Running",
			"Reviewing blocked tickets")
		// TODO: PM could analyze blocked tickets and suggest resolutions
	}

	return nil
}

// PMCheckinStore is the interface for PM check-in persistence.
type PMCheckinStore interface {
	AddPMCheckin(checkin *kanban.PMCheckin) error
	GetLastPMCheckin(ticketID string) (*kanban.PMCheckin, error)
	GetConfigValue(key string) (string, error)
	CreateConversation(conv *kanban.TicketConversation) error
	AddConversationMessage(msg *kanban.ConversationMessage) error
}

// performPMCheckins performs periodic check-ins on active development tickets.
// This runs every 15 minutes (configurable) to review progress, identify blockers,
// and ensure development is on track.
func (m *BackgroundAgentManager) performPMCheckins(ctx context.Context, state kanban.StateStore) {
	// Check if the state supports PM check-ins
	checkinStore, ok := state.(PMCheckinStore)
	if !ok {
		return // Store doesn't support check-ins
	}

	// Get check-in interval from config (default 15 minutes)
	checkinInterval := 15 * time.Minute
	if intervalStr, err := checkinStore.GetConfigValue("pm_checkin_interval"); err == nil && intervalStr != "" {
		if minutes, err := strconv.Atoi(intervalStr); err == nil {
			checkinInterval = time.Duration(minutes) * time.Minute
		}
	}

	// Get all tickets in active development stages
	inDevTickets := state.GetTicketsByStatus(kanban.StatusInDev)
	inQATickets := state.GetTicketsByStatus(kanban.StatusInQA)
	inUXTickets := state.GetTicketsByStatus(kanban.StatusInUX)
	inSecTickets := state.GetTicketsByStatus(kanban.StatusInSec)

	// Combine all active tickets
	activeTickets := append(inDevTickets, inQATickets...)
	activeTickets = append(activeTickets, inUXTickets...)
	activeTickets = append(activeTickets, inSecTickets...)

	for _, ticket := range activeTickets {
		// Check if we need to perform a check-in for this ticket
		lastCheckin, _ := checkinStore.GetLastPMCheckin(ticket.ID)
		if lastCheckin != nil && time.Since(lastCheckin.CreatedAt) < checkinInterval {
			continue // Not time for a check-in yet
		}

		m.updateAgentStatus(m.agents[BackgroundPM], "Running",
			"Checking in on: "+ticket.ID)

		// Perform the check-in
		m.performSingleCheckin(ctx, checkinStore, state, &ticket)
	}
}

// performSingleCheckin performs a PM check-in on a single ticket.
func (m *BackgroundAgentManager) performSingleCheckin(
	ctx context.Context,
	checkinStore PMCheckinStore,
	state kanban.StateStore,
	ticket *kanban.Ticket,
) {
	// Analyze the ticket's current state
	findings := m.analyzeTicketProgress(ticket, state)

	// Determine check-in type and summary
	checkinType := kanban.CheckinTypeProgress
	summary := fmt.Sprintf("Progress check on ticket %s (%s)", ticket.ID, ticket.Status)
	actionRequired := ""

	// Check for concerning patterns
	if findings.ProgressPercent < 10 && time.Since(ticket.UpdatedAt) > 1*time.Hour {
		checkinType = kanban.CheckinTypeGuidance
		summary = fmt.Sprintf("Ticket %s appears to have stalled - limited progress detected", ticket.ID)
		actionRequired = "Review agent activity and consider providing additional guidance"
	}

	if len(findings.Blockers) > 0 {
		checkinType = kanban.CheckinTypeBlocker
		summary = fmt.Sprintf("Ticket %s has blockers: %v", ticket.ID, findings.Blockers)
		actionRequired = "Address blockers to unblock development"
	}

	if len(findings.Concerns) > 0 {
		checkinType = kanban.CheckinTypeReview
		summary = fmt.Sprintf("Ticket %s has concerns requiring review", ticket.ID)
		actionRequired = "Review concerns and provide guidance if needed"
	}

	// Create the check-in record
	checkin := &kanban.PMCheckin{
		ID:             fmt.Sprintf("checkin-%s-%d", ticket.ID, time.Now().Unix()),
		TicketID:       ticket.ID,
		CheckinType:    checkinType,
		Summary:        summary,
		Findings:       &findings,
		ActionRequired: actionRequired,
		Resolved:       false,
		CreatedAt:      time.Now(),
	}

	// If there are concerns or blockers, create a conversation thread
	if checkinType != kanban.CheckinTypeProgress {
		convID := m.createCheckinConversation(checkinStore, ticket, checkin)
		checkin.ConversationID = convID
	}

	// Save the check-in
	if err := checkinStore.AddPMCheckin(checkin); err != nil {
		m.orchestrator.logger.Error("Failed to record PM check-in",
			"ticket", ticket.ID,
			"error", err)
		return
	}

	m.orchestrator.logger.Info("PM check-in completed",
		"ticket", ticket.ID,
		"type", checkinType,
		"summary", summary)
}

// analyzeTicketProgress analyzes a ticket's progress and returns findings.
func (m *BackgroundAgentManager) analyzeTicketProgress(ticket *kanban.Ticket, state kanban.StateStore) kanban.PMCheckinFindings {
	findings := kanban.PMCheckinFindings{
		ProgressPercent: 50, // Default to 50% if we can't determine
		Concerns:        []string{},
		Blockers:        []string{},
	}

	// Check time in current status
	timeSinceUpdate := time.Since(ticket.UpdatedAt)

	// Estimate progress based on status
	switch ticket.Status {
	case kanban.StatusInDev:
		if timeSinceUpdate < 30*time.Minute {
			findings.ProgressPercent = 25
		} else if timeSinceUpdate < 2*time.Hour {
			findings.ProgressPercent = 50
		} else if timeSinceUpdate < 4*time.Hour {
			findings.ProgressPercent = 75
		} else {
			findings.ProgressPercent = 90
			findings.Concerns = append(findings.Concerns, "Development taking longer than expected")
		}
	case kanban.StatusInQA:
		findings.ProgressPercent = 70
	case kanban.StatusInUX:
		findings.ProgressPercent = 80
	case kanban.StatusInSec:
		findings.ProgressPercent = 90
	}

	// Check for bugs
	if len(ticket.Bugs) > 0 {
		for _, bug := range ticket.Bugs {
			if bug.Severity == "critical" || bug.Severity == "high" {
				findings.Blockers = append(findings.Blockers, fmt.Sprintf("Bug: %s (%s)", bug.Description, bug.Severity))
			} else {
				findings.Concerns = append(findings.Concerns, fmt.Sprintf("Bug: %s (%s)", bug.Description, bug.Severity))
			}
		}
	}

	// Check if agent is actively running
	activeRuns := state.GetActiveRunsForTicket(ticket.ID)
	if len(activeRuns) == 0 && ticket.Status == kanban.StatusInDev {
		findings.Concerns = append(findings.Concerns, "No active agent running for ticket in IN_DEV status")
	}

	// Check worktree status
	if ticket.Worktree == nil && ticket.Status == kanban.StatusInDev {
		findings.Blockers = append(findings.Blockers, "No worktree created for development")
	}

	return findings
}

// createCheckinConversation creates a conversation thread for a PM check-in.
func (m *BackgroundAgentManager) createCheckinConversation(
	store PMCheckinStore,
	ticket *kanban.Ticket,
	checkin *kanban.PMCheckin,
) string {
	convID := fmt.Sprintf("conv-%s-%d", ticket.ID, time.Now().Unix())

	// Create the conversation thread
	conv := &kanban.TicketConversation{
		ID:         convID,
		TicketID:   ticket.ID,
		ThreadType: kanban.ThreadTypePMCheckin,
		Title:      fmt.Sprintf("PM Check-in: %s", checkin.CheckinType),
		Status:     kanban.ThreadStatusOpen,
		CreatedAt:  time.Now(),
	}

	if err := store.CreateConversation(conv); err != nil {
		m.orchestrator.logger.Error("Failed to create check-in conversation",
			"ticket", ticket.ID,
			"error", err)
		return ""
	}

	// Add the initial message
	metadataMap := map[string]interface{}{
		"checkin_type":    string(checkin.CheckinType),
		"action_required": checkin.ActionRequired,
		"findings":        checkin.Findings,
	}
	metadataJSON, _ := json.Marshal(metadataMap)

	msg := &kanban.ConversationMessage{
		ID:             fmt.Sprintf("msg-%s-1", convID),
		ConversationID: convID,
		Agent:          "PM",
		MessageType:    kanban.MessageTypeStatusUpdate,
		Content:        checkin.Summary,
		Metadata:       string(metadataJSON),
		CreatedAt:      time.Now(),
	}

	if err := store.AddConversationMessage(msg); err != nil {
		m.orchestrator.logger.Error("Failed to add check-in message",
			"conversation", convID,
			"error", err)
	}

	return convID
}

// runSecurityBackground is the Security agent's background work loop.
// It proactively scans for security issues.
func (m *BackgroundAgentManager) runSecurityBackground(ctx context.Context) error {
	m.updateAgentStatus(m.agents[BackgroundSecurity], "Running", "Scanning for security issues")

	// Security agent can:
	// 1. Review recently completed tickets for security concerns
	// 2. Scan worktrees for common vulnerabilities
	// 3. Check dependencies for known CVEs
	// 4. Review commit history for sensitive data

	state := m.orchestrator.state

	// Look at tickets that have passed security review
	doneTickets := state.GetTicketsByStatus(kanban.StatusDone)
	if len(doneTickets) > 0 {
		m.updateAgentStatus(m.agents[BackgroundSecurity], "Running",
			"Reviewing completed work for security regressions")
	}

	// Check active worktrees for security issues
	activeRuns := state.GetActiveRuns()
	for _, run := range activeRuns {
		if run.Worktree != "" {
			m.updateAgentStatus(m.agents[BackgroundSecurity], "Running",
				"Scanning worktree: "+run.TicketID)
			// TODO: Run security scanning tools on worktree
		}
	}

	return nil
}

// runGathererBackground is the Gatherer/Ideas agent's background work loop.
// It looks for improvement opportunities and potential work items.
func (m *BackgroundAgentManager) runGathererBackground(ctx context.Context) error {
	m.updateAgentStatus(m.agents[BackgroundGatherer], "Running", "Gathering ideas and improvements")

	// Gatherer agent can:
	// 1. Scan codebase for TODO/FIXME comments
	// 2. Analyze linting/static analysis results
	// 3. Look for code duplication
	// 4. Check for outdated dependencies
	// 5. Monitor error logs for recurring issues

	m.updateAgentStatus(m.agents[BackgroundGatherer], "Running", "Scanning for TODO comments")

	// TODO: Implement code scanning
	// - Find TODO/FIXME/HACK comments
	// - Run linters and collect warnings
	// - Check for dependency updates
	// - Create new backlog items via the dashboard

	m.updateAgentStatus(m.agents[BackgroundGatherer], "Running", "Analyzing code quality")

	return nil
}

// healStuckDevTickets detects tickets stuck in IN_DEV with no active agent and resets them.
// This handles cases where an agent failed but didn't properly reset the ticket status.
func (m *BackgroundAgentManager) healStuckDevTickets(state kanban.StateStore, activeRuns []kanban.AgentRun) {
	// Build a set of ticket IDs that have actively running agents
	activeTickets := make(map[string]bool)
	for _, run := range activeRuns {
		if run.Status == "running" {
			activeTickets[run.TicketID] = true
		}
	}

	// Find IN_DEV tickets without an active running agent
	inDevTickets := state.GetTicketsByStatus(kanban.StatusInDev)
	for _, ticket := range inDevTickets {
		if activeTickets[ticket.ID] {
			continue // This ticket has an active agent, skip
		}

		// Ticket is stuck - no active agent running
		m.orchestrator.logger.Warn("PM: Detected stuck IN_DEV ticket with no active agent",
			"ticket", ticket.ID,
			"title", ticket.Title,
			"assignee", ticket.AssignedAgent)

		m.updateAgentStatus(m.agents[BackgroundPM], "Running",
			"Self-healing stuck ticket: "+ticket.ID)

		// Reset ticket back to READY for retry
		state.UpdateTicketStatus(ticket.ID, kanban.StatusReady, "PM-SelfHeal",
			"Ticket was stuck in IN_DEV with no active agent - resetting for retry")

		// Clear the assigned agent and worktree
		state.ClearActivity(ticket.ID)

		// Log the recovery
		m.orchestrator.logger.Info("PM: Self-healed stuck ticket - reset to READY",
			"ticket", ticket.ID)
	}

	// Save state if any tickets were healed
	if len(inDevTickets) > len(activeTickets) {
		state.Save()
	}
}
