package factory

import (
	"context"
	"sync"
	"time"

	"github.com/arctek/factory/kanban"
)

// BackgroundAgentType represents a type of always-running background agent.
type BackgroundAgentType string

const (
	BackgroundPM       BackgroundAgentType = "PM"
	BackgroundSecurity BackgroundAgentType = "Security"
	BackgroundGatherer BackgroundAgentType = "Gatherer"
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
