// Package factory implements the AI development factory orchestrator.
package factory

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/madhatter5501/Factory/agents"
	"github.com/madhatter5501/Factory/git"
	"github.com/madhatter5501/Factory/kanban"
)

// Orchestrator coordinates the AI development factory.
type Orchestrator struct {
	// Configuration
	repoRoot   string
	promptsDir string
	config     Config

	// Components
	state          kanban.StateStore
	worktree       *git.WorktreeManager
	spawner        agents.AgentSpawner
	spawnerFactory *agents.SpawnerFactory
	backgroundMgr  *BackgroundAgentManager

	// Runtime
	logger     *slog.Logger
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex

	// Metrics
	metrics Metrics
}

// Config holds orchestrator configuration.
type Config struct {
	// Paths
	WorktreeDir string `json:"worktreeDir"`
	MainBranch  string `json:"mainBranch"`
	BareRepo    string `json:"bareRepo"` // Optional bare repo for local-only workflow

	// Limits
	MaxParallelAgents int           `json:"maxParallelAgents"`
	AgentTimeout      time.Duration `json:"agentTimeout"`
	CycleInterval     time.Duration `json:"cycleInterval"`

	// Behavior
	AutoMerge     bool `json:"autoMerge"`     // Auto-merge completed tickets
	AutoCleanup   bool `json:"autoCleanup"`   // Auto-cleanup merged worktrees
	Verbose       bool `json:"verbose"`       // Verbose logging
	DryRun        bool `json:"dryRun"`        // Don't actually run agents

	// API Mode Configuration (for token efficiency)
	SpawnerMode    agents.SpawnerMode `json:"spawnerMode"`    // "cli", "api", or "auto"
	RAGEnabled     bool               `json:"ragEnabled"`     // Enable RAG for dynamic context
	VectorDBPath   string             `json:"vectorDbPath"`   // Path to RAG vector database
	Model          string             `json:"model"`          // Model override (default: claude-sonnet-4)
	IndexOnStartup bool               `json:"indexOnStartup"` // Index prompts on startup
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		WorktreeDir:       ".worktrees",
		MainBranch:        "main",
		MaxParallelAgents: 3,
		AgentTimeout:      30 * time.Minute,
		CycleInterval:     10 * time.Second,
		AutoMerge:         false, // Require manual merge for safety
		AutoCleanup:       true,
		Verbose:           true,
		DryRun:            false,
		// API mode defaults - auto-detect based on ANTHROPIC_API_KEY
		SpawnerMode:    agents.SpawnerModeAuto,
		RAGEnabled:     true,
		VectorDBPath:   "rag.db",
		IndexOnStartup: true,
	}
}

// Metrics tracks orchestrator statistics.
type Metrics struct {
	CyclesRun         int           `json:"cyclesRun"`
	AgentsSpawned     int           `json:"agentsSpawned"`
	AgentsSucceeded   int           `json:"agentsSucceeded"`
	AgentsFailed      int           `json:"agentsFailed"`
	TicketsCompleted  int           `json:"ticketsCompleted"`
	TotalRuntime      time.Duration `json:"totalRuntime"`
}

// NewOrchestrator creates a new orchestrator with the provided state store.
func NewOrchestrator(repoRoot string, config Config, state kanban.StateStore) (*Orchestrator, error) {
	// Look for prompts in ./prompts/ first (when running from factory dir),
	// then fall back to agents/factory/prompts/ (when running from monorepo root)
	promptsDir := filepath.Join(repoRoot, "prompts")
	if _, err := os.Stat(promptsDir); os.IsNotExist(err) {
		promptsDir = filepath.Join(repoRoot, "agents", "factory", "prompts")
	}

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Create components
	worktree := git.NewWorktreeManager(repoRoot, config.WorktreeDir, config.MainBranch)
	if config.BareRepo != "" {
		worktree.SetBareRepo(config.BareRepo)
	}

	// Create spawner using factory (supports CLI and API modes)
	spawnerConfig := agents.SpawnerConfig{
		Mode:           config.SpawnerMode,
		PromptsDir:     promptsDir,
		Timeout:        config.AgentTimeout,
		Verbose:        config.Verbose,
		Model:          config.Model,
		RAGEnabled:     config.RAGEnabled,
		VectorDBPath:   config.VectorDBPath,
		IndexOnStartup: config.IndexOnStartup,
	}
	spawnerFactory := agents.NewSpawnerFactory(spawnerConfig)
	spawner, err := spawnerFactory.CreateSpawner()
	if err != nil {
		return nil, fmt.Errorf("failed to create spawner: %w", err)
	}

	logger.Info("Spawner initialized",
		"mode", spawnerFactory.GetMode(),
		"rag_enabled", config.RAGEnabled,
	)

	return &Orchestrator{
		repoRoot:       repoRoot,
		promptsDir:     promptsDir,
		config:         config,
		state:          state,
		worktree:       worktree,
		spawner:        spawner,
		spawnerFactory: spawnerFactory,
		logger:         logger,
	}, nil
}

// Initialize sets up the orchestrator.
func (o *Orchestrator) Initialize() error {
	o.logger.Info("Initializing factory orchestrator")

	// Load kanban state
	if err := o.state.Load(); err != nil {
		return fmt.Errorf("failed to load kanban state: %w", err)
	}

	// Validate environment
	if errors := o.spawner.ValidateAgentEnvironment(); len(errors) > 0 {
		for _, err := range errors {
			o.logger.Warn("Environment issue", "error", err)
		}
	}

	// Cleanup any orphaned worktrees
	if err := o.worktree.CleanupOrphanedWorktrees(); err != nil {
		o.logger.Warn("Failed to cleanup worktrees", "error", err)
	}

	// Create background agent manager
	o.backgroundMgr = NewBackgroundAgentManager(o)

	o.logger.Info("Factory initialized",
		"tickets", len(o.state.GetBoard().Tickets),
		"worktreeDir", o.config.WorktreeDir)

	return nil
}

// Run starts the orchestrator main loop.
func (o *Orchestrator) Run(ctx context.Context) error {
	ctx, o.cancelFunc = context.WithCancel(ctx)
	startTime := time.Now()

	o.logger.Info("Starting factory orchestrator")

	// Start background agents (PM, Security, Gatherer)
	if o.backgroundMgr != nil {
		o.backgroundMgr.Start(ctx)
		defer o.backgroundMgr.Stop()
	}

	ticker := time.NewTicker(o.config.CycleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.logger.Info("Orchestrator shutting down")
			o.wg.Wait()
			o.metrics.TotalRuntime = time.Since(startTime)
			return nil

		case <-ticker.C:
			if err := o.runCycle(ctx); err != nil {
				o.logger.Error("Cycle failed", "error", err)
			}
		}
	}
}

// Stop gracefully stops the orchestrator.
func (o *Orchestrator) Stop() {
	if o.cancelFunc != nil {
		o.cancelFunc()
	}

	// Print token usage report if using API mode
	if o.config.Verbose && o.spawnerFactory != nil {
		agents.PrintUsageReport(o.spawner)
	}
}

// GetTokenUsage returns the current token usage (API mode only).
func (o *Orchestrator) GetTokenUsage() *agents.TokenUsage {
	if o.spawnerFactory != nil {
		return o.spawnerFactory.TokenStats(o.spawner)
	}
	return nil
}

// runCycle executes one orchestration cycle.
func (o *Orchestrator) runCycle(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.metrics.CyclesRun++
	o.logger.Debug("Running cycle", "cycle", o.metrics.CyclesRun)

	// Reload state (in case of external changes)
	if err := o.state.Load(); err != nil {
		return fmt.Errorf("failed to reload state: %w", err)
	}

	// Check if iteration is complete
	if o.state.IsIterationComplete() {
		o.logger.Info("Iteration complete!")
		return nil
	}

	// Get board stats
	stats := o.state.GetStats()
	o.logger.Info("Board status",
		"approved", stats[kanban.StatusApproved],
		"refining", stats[kanban.StatusRefining],
		"needsExpert", stats[kanban.StatusNeedsExpert],
		"awaitingUser", stats[kanban.StatusAwaitingUser],
		"ready", stats[kanban.StatusReady],
		"inDev", stats[kanban.StatusInDev],
		"inQA", stats[kanban.StatusInQA],
		"done", stats[kanban.StatusDone])

	// Process requirements gathering pipeline (legacy linear flow)
	// DISABLED: Now using collaborative PRD model instead
	// o.processApprovedToRefining(ctx)
	// o.processRefiningStage(ctx)
	// o.processExpertConsultationStage(ctx)

	// Process collaborative PRD pipeline (new multi-round flow)
	// 1. Move approved tickets to PRD discussion rounds
	o.processApprovedToPRDRound(ctx)
	// 2. Handle PRD discussion rounds (spawn experts, collect responses)
	o.processPRDRoundStage(ctx)
	// 3. Handle completed PRDs (break down into sub-tickets)
	o.processPRDCompleteStage(ctx)
	// 4. Check if parent tickets should be marked complete
	o.checkParentCompletion(ctx)

	// Process development pipeline
	o.processDevStage(ctx)
	o.processQAStage(ctx)
	o.processUXStage(ctx)
	o.processSecurityStage(ctx)
	o.processPMReviewStage(ctx)

	// Handle completed tickets
	if o.config.AutoMerge {
		o.processCompletedTickets(ctx)
	}

	// Save state
	if err := o.state.Save(); err != nil {
		o.logger.Error("Failed to save state", "error", err)
	}

	return nil
}

// processApprovedToRefining moves newly approved tickets into requirements refinement.
func (o *Orchestrator) processApprovedToRefining(ctx context.Context) {
	approvedTickets := o.state.GetTicketsByStatus(kanban.StatusApproved)
	if len(approvedTickets) == 0 {
		return
	}

	o.logger.Info("Starting requirements refinement for approved tickets", "count", len(approvedTickets))

	for _, ticket := range approvedTickets {
		// Initialize requirements tracking
		if ticket.Requirements == nil {
			ticket.Requirements = &kanban.Requirements{
				StartedAt: time.Now(),
			}
		}

		// Move to REFINING status
		o.state.UpdateTicketStatus(ticket.ID, kanban.StatusRefining, "PM", "Starting requirements analysis")
		o.state.Save()

		o.logger.Info("Ticket moved to requirements refinement", "ticket", ticket.ID)
	}
}

// processRefiningStage runs PM requirements analysis on tickets being refined.
func (o *Orchestrator) processRefiningStage(ctx context.Context) {
	refiningTickets := o.state.GetTicketsByStatus(kanban.StatusRefining)
	if len(refiningTickets) == 0 {
		return
	}

	o.logger.Info("Processing tickets in refinement", "count", len(refiningTickets))

	for _, ticket := range refiningTickets {
		// Update activity
		o.state.UpdateActivity(ticket.ID, "PM analyzing requirements", "PM")

		if o.config.DryRun {
			o.logger.Info("[DRY RUN] Would run PM requirements analysis", "ticket", ticket.ID)
			continue
		}

		// Run PM requirements analysis agent
		promptData := agents.PromptData{
			Ticket:     &ticket,
			BoardStats: o.state.GetStats(),
		}

		result, err := o.spawner.SpawnAgent(ctx, agents.AgentTypePMRequirements, promptData, o.repoRoot)
		if err != nil {
			o.logger.Error("PM requirements analysis failed", "ticket", ticket.ID, "error", err)
			continue
		}

		// Parse the result to determine next action
		nextStatus, expertDomain := o.parseRequirementsResult(result)

		switch nextStatus {
		case kanban.StatusNeedsExpert:
			// Store which domain needs consultation
			if ticket.Requirements == nil {
				ticket.Requirements = &kanban.Requirements{}
			}
			ticket.Requirements.Questions = append(ticket.Requirements.Questions, kanban.Question{Question: fmt.Sprintf("Needs %s expert consultation", expertDomain)})
			o.state.UpdateTicketStatus(ticket.ID, kanban.StatusNeedsExpert, "PM", fmt.Sprintf("Needs %s expert input", expertDomain))

		case kanban.StatusAwaitingUser:
			// Requirements compiled, waiting for user review
			o.state.UpdateTicketStatus(ticket.ID, kanban.StatusAwaitingUser, "PM", "Requirements ready for user review")

		case kanban.StatusReady:
			// Requirements are clear enough to proceed directly
			o.state.UpdateTicketStatus(ticket.ID, kanban.StatusReady, "PM", "Requirements clear, ready for development")

		default:
			// Stay in REFINING if analysis is incomplete
			o.logger.Debug("Ticket staying in refinement", "ticket", ticket.ID)
		}

		o.state.ClearActivity(ticket.ID)
		o.state.Save()
	}
}

// processExpertConsultationStage handles tickets waiting for domain expert input.
func (o *Orchestrator) processExpertConsultationStage(ctx context.Context) {
	expertTickets := o.state.GetTicketsByStatus(kanban.StatusNeedsExpert)
	if len(expertTickets) == 0 {
		return
	}

	o.logger.Info("Processing expert consultation requests", "count", len(expertTickets))

	for _, ticket := range expertTickets {
		// Determine which domain expert to consult based on ticket domain
		expertDomain := string(ticket.Domain)
		if expertDomain == "" {
			expertDomain = "backend"
		}

		o.state.UpdateActivity(ticket.ID, fmt.Sprintf("Consulting %s expert", expertDomain), "Expert")

		if o.config.DryRun {
			o.logger.Info("[DRY RUN] Would run expert consultation", "ticket", ticket.ID, "domain", expertDomain)
			continue
		}

		// Gather questions from requirements
		var questions []string
		if ticket.Requirements != nil {
			for _, q := range ticket.Requirements.Questions {
				questions = append(questions, q.Question)
			}
		}

		// Run expert consultation agent
		promptData := agents.PromptData{
			Ticket:     &ticket,
			Domain:     expertDomain,
			Questions:  questions,
			BoardStats: o.state.GetStats(),
		}

		result, err := o.spawner.SpawnAgent(ctx, agents.AgentTypeExpertConsult, promptData, o.repoRoot)
		if err != nil {
			o.logger.Error("Expert consultation failed", "ticket", ticket.ID, "error", err)
			continue
		}

		// Record the consultation
		if ticket.Requirements == nil {
			ticket.Requirements = &kanban.Requirements{}
		}
		ticket.Requirements.Consultations = append(ticket.Requirements.Consultations, kanban.ExpertConsultation{
			Domain:     kanban.Domain(expertDomain),
			Question:   "See requirements questions",
			Response:   result.Output,
			AskedAt:    time.Now(),
			AnsweredAt: time.Now(),
		})
		ticket.Requirements.TechnicalNotes = result.Output

		// Move back to REFINING for PM to compile final requirements
		o.state.UpdateTicketStatus(ticket.ID, kanban.StatusRefining, "Expert", "Expert consultation complete, resuming analysis")
		o.state.ClearActivity(ticket.ID)
		o.state.Save()

		o.logger.Info("Expert consultation complete", "ticket", ticket.ID, "domain", expertDomain)
	}
}

// parseRequirementsResult analyzes PM requirements agent output to determine next status.
func (o *Orchestrator) parseRequirementsResult(result *agents.AgentResult) (kanban.Status, string) {
	output := result.Output

	// Look for decision markers in the output
	if containsAny(output, "NEEDS_EXPERT:", "NEEDS_EXPERT") {
		// Extract domain if specified
		domain := "backend" // default
		if containsAny(output, "frontend", "FRONTEND") {
			domain = "frontend"
		} else if containsAny(output, "infra", "INFRA", "infrastructure") {
			domain = "infra"
		}
		return kanban.StatusNeedsExpert, domain
	}

	if containsAny(output, "NEEDS_USER_INPUT", "needs user", "unclear requirements") {
		return kanban.StatusAwaitingUser, ""
	}

	if containsAny(output, "READY_FOR_DEV", "ready for development", "requirements clear") {
		return kanban.StatusReady, ""
	}

	// Default: needs user review (safest option)
	return kanban.StatusAwaitingUser, ""
}

// containsAny checks if the text contains any of the substrings (case-insensitive).
func containsAny(text string, substrings ...string) bool {
	lowerText := strings.ToLower(text)
	for _, s := range substrings {
		if strings.Contains(lowerText, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// processDevStage handles tickets ready for development.
// Dev agents are limited to MaxParallelAgents (default 3).
// Other agents (QA, UX, Security, PM) run without limits.
func (o *Orchestrator) processDevStage(ctx context.Context) {
	// Check if we can spawn more DEV agents (only dev agents count toward the limit)
	activeDevRuns := o.state.GetActiveDevRuns()
	if len(activeDevRuns) >= o.config.MaxParallelAgents {
		o.logger.Debug("Dev agent limit reached", "active", len(activeDevRuns), "limit", o.config.MaxParallelAgents)
		return
	}

	// Get ready tickets by domain
	domains := []kanban.Domain{
		kanban.DomainFrontend,
		kanban.DomainBackend,
		kanban.DomainInfra,
	}

	for _, domain := range domains {
		// Check dev-specific parallel limit
		if len(o.state.GetActiveDevRuns()) >= o.config.MaxParallelAgents {
			break
		}

		ticket, ok := o.state.GetNextTicketForDomain(domain)
		if !ok {
			continue
		}

		// Start dev agent for this ticket
		o.wg.Add(1)
		go func(t *kanban.Ticket, d kanban.Domain) {
			defer o.wg.Done()
			o.runDevAgent(ctx, t, d)
		}(ticket, domain)
	}

	// Also process tickets without a domain (e.g., from Notion without domain set)
	// Default these to backend agent
	readyTickets := o.state.GetTicketsByStatus(kanban.StatusReady)
	for _, ticket := range readyTickets {
		if len(o.state.GetActiveDevRuns()) >= o.config.MaxParallelAgents {
			break
		}

		// Skip tickets that already have a known domain (handled above)
		if ticket.Domain == kanban.DomainFrontend ||
			ticket.Domain == kanban.DomainBackend ||
			ticket.Domain == kanban.DomainInfra {
			continue
		}

		// Default to backend for unspecified domains
		defaultDomain := kanban.DomainBackend
		o.wg.Add(1)
		go func(t kanban.Ticket, d kanban.Domain) {
			defer o.wg.Done()
			o.runDevAgent(ctx, &t, d)
		}(ticket, defaultDomain)
	}
}

// runDevAgent runs a development agent for a ticket.
func (o *Orchestrator) runDevAgent(ctx context.Context, ticket *kanban.Ticket, domain kanban.Domain) {
	agentType := agents.GetAgentTypeForDomain(domain)
	o.logger.Info("Starting dev agent",
		"ticket", ticket.ID,
		"domain", domain,
		"agent", agentType)

	// Create worktree
	branchName := git.GenerateBranchName(
		o.state.GetConfig().BranchPrefix,
		ticket.ID,
		ticket.Title,
	)

	worktreePath, err := o.worktree.CreateWorktree(ticket.ID, branchName)
	if err != nil {
		o.logger.Error("Failed to create worktree", "ticket", ticket.ID, "error", err)
		return
	}

	// Update ticket state and activity
	activityDescription := getActivityDescription(agentType)
	o.state.UpdateTicketStatus(ticket.ID, kanban.StatusInDev, string(agentType), "Starting development")
	o.state.AssignAgent(ticket.ID, string(agentType))
	o.state.UpdateActivity(ticket.ID, activityDescription, string(agentType))
	o.state.SetWorktree(ticket.ID, &kanban.Worktree{
		Path:   worktreePath,
		Branch: branchName,
		Active: true,
	})
	o.state.Save()

	// Record run
	runID := fmt.Sprintf("%s-%s-%d", ticket.ID, agentType, time.Now().Unix())
	o.state.AddActiveRun(kanban.AgentRun{
		ID:        runID,
		Agent:     string(agentType),
		TicketID:  ticket.ID,
		Worktree:  worktreePath,
		StartedAt: time.Now(),
		Status:    "running",
	})

	// Spawn agent
	if !o.config.DryRun {
		result, err := o.spawner.SpawnAgent(ctx, agentType, agents.PromptData{
			Ticket:       ticket,
			WorktreePath: worktreePath,
			Domain:       string(domain),
			BoardStats:   o.state.GetStats(),
			Iteration:    o.state.GetIteration(),
		}, worktreePath)

		o.metrics.AgentsSpawned++

		if err != nil || !result.Success {
			o.logger.Error("Dev agent failed",
				"ticket", ticket.ID,
				"error", err,
				"output", result.Error)
			o.metrics.AgentsFailed++
			o.state.CompleteRun(runID, "failed", result.Error)

			return
		}

		o.metrics.AgentsSucceeded++
		o.state.CompleteRun(runID, "success", result.Output)
	}

	// Clear activity and transition to QA
	o.state.ClearActivity(ticket.ID)
	o.state.AddSignoff(ticket.ID, "dev", string(agentType))
	o.state.UpdateTicketStatus(ticket.ID, kanban.StatusInQA, string(agentType), "Development complete, ready for QA")
	o.state.Save()

	o.logger.Info("Dev agent completed", "ticket", ticket.ID)
}

// processQAStage handles tickets in QA.
func (o *Orchestrator) processQAStage(ctx context.Context) {
	tickets := o.state.GetTicketsByStatus(kanban.StatusInQA)

	for _, ticket := range tickets {
		// Check if QA agent is already running for this ticket
		running := false
		for _, run := range o.state.GetActiveRuns() {
			if run.TicketID == ticket.ID && run.Agent == string(agents.AgentTypeQA) {
				running = true
				break
			}
		}
		if running {
			continue
		}

		o.wg.Add(1)
		go func(t kanban.Ticket) {
			defer o.wg.Done()
			o.runReviewAgent(ctx, &t, agents.AgentTypeQA, kanban.StatusInUX, "qa")
		}(ticket)
	}
}

// processUXStage handles tickets in UX review.
func (o *Orchestrator) processUXStage(ctx context.Context) {
	tickets := o.state.GetTicketsByStatus(kanban.StatusInUX)

	for _, ticket := range tickets {
		o.wg.Add(1)
		go func(t kanban.Ticket) {
			defer o.wg.Done()
			o.runReviewAgent(ctx, &t, agents.AgentTypeUX, kanban.StatusInSec, "ux")
		}(ticket)
	}
}

// processSecurityStage handles tickets in security review.
func (o *Orchestrator) processSecurityStage(ctx context.Context) {
	tickets := o.state.GetTicketsByStatus(kanban.StatusInSec)

	for _, ticket := range tickets {
		o.wg.Add(1)
		go func(t kanban.Ticket) {
			defer o.wg.Done()
			o.runReviewAgent(ctx, &t, agents.AgentTypeSecurity, kanban.StatusPMReview, "security")
		}(ticket)
	}
}

// processPMReviewStage handles tickets awaiting PM review.
func (o *Orchestrator) processPMReviewStage(ctx context.Context) {
	tickets := o.state.GetTicketsByStatus(kanban.StatusPMReview)

	for _, ticket := range tickets {
		o.wg.Add(1)
		go func(t kanban.Ticket) {
			defer o.wg.Done()
			o.runReviewAgent(ctx, &t, agents.AgentTypePM, kanban.StatusDone, "pm")
		}(ticket)
	}
}

// runReviewAgent runs a review agent (QA, UX, Security, PM).
func (o *Orchestrator) runReviewAgent(ctx context.Context, ticket *kanban.Ticket, agentType agents.AgentType, nextStatus kanban.Status, signoffStage string) {
	o.logger.Info("Starting review agent",
		"ticket", ticket.ID,
		"agent", agentType)

	// Update activity to show what agent is doing
	activityDescription := getActivityDescription(agentType)
	o.state.UpdateActivity(ticket.ID, activityDescription, string(agentType))
	o.state.Save()

	worktreePath := ""
	if ticket.Worktree != nil {
		worktreePath = ticket.Worktree.Path
	}

	if !o.config.DryRun {
		result, err := o.spawner.SpawnAgent(ctx, agentType, agents.PromptData{
			Ticket:       ticket,
			WorktreePath: worktreePath,
			BoardStats:   o.state.GetStats(),
			Iteration:    o.state.GetIteration(),
		}, worktreePath)

		o.metrics.AgentsSpawned++

		if err != nil || !result.Success {
			o.logger.Error("Review agent failed",
				"ticket", ticket.ID,
				"agent", agentType,
				"error", err)
			o.metrics.AgentsFailed++

			// Check if bugs were found
			if len(ticket.Bugs) > 0 {
				o.state.UpdateTicketStatus(ticket.ID, kanban.StatusBlocked, string(agentType), "Bugs found during review")
			}
			return
		}

		o.metrics.AgentsSucceeded++
	}

	// Clear activity, sign off and transition
	o.state.ClearActivity(ticket.ID)
	o.state.AddSignoff(ticket.ID, signoffStage, string(agentType))
	o.state.UpdateTicketStatus(ticket.ID, nextStatus, string(agentType), fmt.Sprintf("%s review complete", agentType))
	o.state.Save()

	if nextStatus == kanban.StatusDone {
		o.metrics.TicketsCompleted++
	}

	o.logger.Info("Review agent completed", "ticket", ticket.ID, "agent", agentType)
}

// getReviewTypeName returns a human-readable name for the review type.
func getReviewTypeName(agentType agents.AgentType) string {
	switch agentType {
	case agents.AgentTypeQA:
		return "QA"
	case agents.AgentTypeUX:
		return "UX"
	case agents.AgentTypeSecurity:
		return "Security"
	case agents.AgentTypePM:
		return "PM final"
	default:
		return string(agentType)
	}
}

// getReviewPassedReason returns a reason for passing a review stage.
func getReviewPassedReason(agentType agents.AgentType) string {
	switch agentType {
	case agents.AgentTypeQA:
		return "All tests passed and code quality meets standards"
	case agents.AgentTypeUX:
		return "User experience reviewed and approved"
	case agents.AgentTypeSecurity:
		return "Security audit completed, no vulnerabilities found"
	case agents.AgentTypePM:
		return "Final review passed, ticket ready for completion"
	default:
		return "Review completed successfully"
	}
}

// getStatusName returns a human-readable name for a status.
func getStatusName(status kanban.Status) string {
	switch status {
	case kanban.StatusBacklog:
		return "Backlog"
	case kanban.StatusApproved:
		return "Approved"
	case kanban.StatusReady:
		return "Ready"
	case kanban.StatusInDev:
		return "In Development"
	case kanban.StatusInQA:
		return "In QA"
	case kanban.StatusInUX:
		return "In UX Review"
	case kanban.StatusInSec:
		return "In Security Review"
	case kanban.StatusPMReview:
		return "PM Review"
	case kanban.StatusDone:
		return "Done"
	case kanban.StatusBlocked:
		return "Blocked"
	default:
		return string(status)
	}
}

// getActivityDescription returns a human-readable description of what an agent is doing.
func getActivityDescription(agentType agents.AgentType) string {
	switch agentType {
	case agents.AgentTypeDevFrontend:
		return "Implementing frontend changes"
	case agents.AgentTypeDevBackend:
		return "Implementing backend logic"
	case agents.AgentTypeDevInfra:
		return "Configuring infrastructure"
	case agents.AgentTypeQA:
		return "Running tests and quality checks"
	case agents.AgentTypeUX:
		return "Reviewing user experience"
	case agents.AgentTypeSecurity:
		return "Performing security audit"
	case agents.AgentTypePM:
		return "Final review and approval"
	default:
		return "Working on task"
	}
}

// processCompletedTickets handles merging completed tickets.
func (o *Orchestrator) processCompletedTickets(ctx context.Context) {
	tickets := o.state.GetTicketsByStatus(kanban.StatusDone)

	for _, ticket := range tickets {
		if ticket.Worktree == nil || !ticket.Worktree.Active {
			continue
		}

		o.logger.Info("Merging completed ticket", "ticket", ticket.ID)

		// Squash merge
		commitMsg := fmt.Sprintf("feat(%s): %s\n\nTicket: %s\nReviewed-by: QA, UX, Security, PM",
			ticket.Domain, ticket.Title, ticket.ID)

		if err := o.worktree.SquashMerge(ticket.Worktree.Branch, commitMsg); err != nil {
			o.logger.Error("Failed to merge", "ticket", ticket.ID, "error", err)
			continue
		}

		// Push main
		if err := o.worktree.PushMain(); err != nil {
			o.logger.Error("Failed to push", "ticket", ticket.ID, "error", err)
			continue
		}

		// Cleanup worktree
		if o.config.AutoCleanup {
			if err := o.worktree.RemoveWorktree(ticket.Worktree.Path, true); err != nil {
				o.logger.Warn("Failed to cleanup worktree", "error", err)
			}
		}

		// Update ticket
		o.state.SetWorktree(ticket.ID, &kanban.Worktree{
			Path:   ticket.Worktree.Path,
			Branch: ticket.Worktree.Branch,
			Active: false,
		})

		o.logger.Info("Ticket merged", "ticket", ticket.ID)
	}
}

// GetMetrics returns current metrics.
func (o *Orchestrator) GetMetrics() Metrics {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.metrics
}

// GetState returns the kanban state store.
func (o *Orchestrator) GetState() kanban.StateStore {
	return o.state
}

// GetBackgroundAgentStatuses returns the status of all background agents.
func (o *Orchestrator) GetBackgroundAgentStatuses() []BackgroundAgentStatus {
	if o.backgroundMgr == nil {
		return nil
	}
	return o.backgroundMgr.GetStatuses()
}

