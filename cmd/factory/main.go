// Factory is an AI development pipeline orchestrator.
// It coordinates multiple AI agents (PM, Dev, QA, UX, Security) to autonomously
// develop software through a kanban-based workflow.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arctek/factory"
	"github.com/arctek/factory/internal/db"
	"github.com/arctek/factory/internal/web"
	"github.com/arctek/factory/kanban"
)

var (
	version   = "dev"
	gitCommit = "unknown"
	buildTime = "unknown"
)

func main() {
	// Parse flags
	var (
		repoRoot      = flag.String("repo", ".", "Repository root path")
		bareRepo      = flag.String("bare-repo", "", "Bare repo path for local-only workflow (no remote auth needed)")
		maxAgents     = flag.Int("max-agents", 3, "Maximum parallel agents")
		timeout       = flag.Duration("timeout", 30*time.Minute, "Agent timeout")
		interval      = flag.Duration("interval", 10*time.Second, "Cycle interval")
		autoMerge     = flag.Bool("auto-merge", false, "Auto-merge completed tickets")
		verbose       = flag.Bool("verbose", true, "Verbose output")
		dryRun        = flag.Bool("dry-run", false, "Don't actually run agents")
		showVersion   = flag.Bool("version", false, "Show version")
		initBoard     = flag.Bool("init", false, "Initialize a new kanban board")
		status        = flag.Bool("status", false, "Show board status")
		dashboard     = flag.Bool("dashboard", false, "Start web dashboard server only")
		withDashboard = flag.Bool("with-dashboard", false, "Run agents with embedded dashboard")
		dashboardPort = flag.String("port", "8080", "Dashboard server port")
		dbPath        = flag.String("db", "factory.db", "SQLite database path")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("factory %s (commit: %s, built: %s)\n", version, gitCommit, buildTime)
		os.Exit(0)
	}

	// Handle dashboard mode
	if *dashboard {
		runDashboard(*dbPath, *dashboardPort)
		return
	}

	// Build config
	config := factory.DefaultConfig()
	config.MaxParallelAgents = *maxAgents
	config.AgentTimeout = *timeout
	config.CycleInterval = *interval
	config.AutoMerge = *autoMerge
	config.Verbose = *verbose
	config.DryRun = *dryRun
	config.BareRepo = *bareRepo

	// Open SQLite database
	database, err := db.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Create state store backed by SQLite
	store := db.NewStore(database)

	// Create orchestrator with SQLite store
	orch, err := factory.NewOrchestrator(*repoRoot, config, store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Initialize
	if err := orch.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Initialization error: %v\n", err)
		os.Exit(1)
	}

	// Handle specific commands
	switch {
	case *initBoard:
		runInitBoard(orch)
		return
	case *status:
		runStatusCmd(orch)
		return
	}

	// Run main loop
	ctx, cancel := context.WithCancel(context.Background())

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nReceived shutdown signal...")
		cancel()
	}()

	// Start embedded dashboard if requested
	if *withDashboard {
		go func() {
			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			}))
			server, err := web.NewServer(database, logger)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create dashboard server: %v\n", err)
				return
			}
			if err := server.Start(":" + *dashboardPort); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "Dashboard server error: %v\n", err)
			}
		}()
	}

	fmt.Println(banner())
	fmt.Printf("Starting factory orchestrator (max %d parallel agents)\n", config.MaxParallelAgents)
	if *withDashboard {
		fmt.Printf("Dashboard running at http://localhost:%s\n", *dashboardPort)
	}
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	if err := orch.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Print final metrics
	metrics := orch.GetMetrics()
	fmt.Println()
	fmt.Println("=== Factory Metrics ===")
	fmt.Printf("Cycles run:        %d\n", metrics.CyclesRun)
	fmt.Printf("Agents spawned:    %d\n", metrics.AgentsSpawned)
	fmt.Printf("Agents succeeded:  %d\n", metrics.AgentsSucceeded)
	fmt.Printf("Agents failed:     %d\n", metrics.AgentsFailed)
	fmt.Printf("Tickets completed: %d\n", metrics.TicketsCompleted)
	fmt.Printf("Total runtime:     %s\n", metrics.TotalRuntime.Round(time.Second))
}

func banner() string {
	return `
╔═══════════════════════════════════════════════════════════════╗
║                                                               ║
║     ███████╗ █████╗  ██████╗████████╗ ██████╗ ██████╗ ██╗   ██║
║     ██╔════╝██╔══██╗██╔════╝╚══██╔══╝██╔═══██╗██╔══██╗╚██╗ ██╔║
║     █████╗  ███████║██║        ██║   ██║   ██║██████╔╝ ╚████╔╝║
║     ██╔══╝  ██╔══██║██║        ██║   ██║   ██║██╔══██╗  ╚██╔╝ ║
║     ██║     ██║  ██║╚██████╗   ██║   ╚██████╔╝██║  ██║   ██║  ║
║     ╚═╝     ╚═╝  ╚═╝ ╚═════╝   ╚═╝    ╚═════╝ ╚═╝  ╚═╝   ╚═╝  ║
║                                                               ║
║              AI Development Pipeline Orchestrator             ║
║                                                               ║
╚═══════════════════════════════════════════════════════════════╝
`
}

func runInitBoard(orch *factory.Orchestrator) {
	fmt.Println("Initializing new kanban board...")

	state := orch.GetState()
	board := state.GetBoard()

	if len(board.Tickets) > 0 {
		fmt.Println("Board already has tickets. Use --pm to create a new iteration.")
		return
	}

	// Save empty board
	if err := state.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving board: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Board initialized in database")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Run 'factory --dashboard' to access the web UI")
	fmt.Println("  2. Use the wizard to create tickets")
	fmt.Println("  3. Run 'factory' to start the development pipeline")
}

func runStatusCmd(orch *factory.Orchestrator) {
	state := orch.GetState()
	board := state.GetBoard()
	stats := state.GetStats()

	fmt.Println("=== Factory Status ===")
	fmt.Println()

	if board.Iteration != nil {
		fmt.Printf("Iteration: %s\n", board.Iteration.ID)
		fmt.Printf("Goal: %s\n", board.Iteration.Goal)
		fmt.Printf("Status: %s\n", board.Iteration.Status)
		fmt.Println()
	}

	fmt.Println("Pipeline:")
	fmt.Printf("  BACKLOG:       %d\n", stats[kanban.StatusBacklog])
	fmt.Println("  --- Requirements ---")
	fmt.Printf("  APPROVED:      %d  (awaiting requirements)\n", stats[kanban.StatusApproved])
	fmt.Printf("  REFINING:      %d  (PM analyzing)\n", stats[kanban.StatusRefining])
	fmt.Printf("  NEEDS_EXPERT:  %d  (consulting domain expert)\n", stats[kanban.StatusNeedsExpert])
	fmt.Printf("  AWAITING_USER: %d  (user review needed)\n", stats[kanban.StatusAwaitingUser])
	fmt.Println("  --- Development ---")
	fmt.Printf("  READY:         %d  (ready for dev)\n", stats[kanban.StatusReady])
	fmt.Printf("  IN_DEV:        %d\n", stats[kanban.StatusInDev])
	fmt.Printf("  IN_QA:         %d\n", stats[kanban.StatusInQA])
	fmt.Printf("  IN_UX:         %d\n", stats[kanban.StatusInUX])
	fmt.Printf("  IN_SEC:        %d\n", stats[kanban.StatusInSec])
	fmt.Printf("  PM_REVIEW:     %d\n", stats[kanban.StatusPMReview])
	fmt.Printf("  DONE:          %d\n", stats[kanban.StatusDone])
	fmt.Printf("  BLOCKED:       %d\n", stats[kanban.StatusBlocked])
	fmt.Println()

	// Show active runs
	activeRuns := state.GetActiveRuns()
	if len(activeRuns) > 0 {
		fmt.Println("Active Agents:")
		for _, run := range activeRuns {
			fmt.Printf("  %s on %s (started %s)\n",
				run.Agent, run.TicketID, run.StartedAt.Format(time.RFC3339))
		}
		fmt.Println()
	}

	// Show tickets in progress
	fmt.Println("Tickets in Progress:")
	for _, ticket := range board.Tickets {
		if ticket.Status != kanban.StatusDone && ticket.Status != kanban.StatusBacklog {
			fmt.Printf("  [%s] %s - %s (%s)\n",
				ticket.ID, ticket.Title, ticket.Status, ticket.Domain)
		}
	}
}

func runDashboard(dbPath, port string) {
	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Open database
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Create server
	server, err := web.NewServer(database, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}

	// Handle signals
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}()

	fmt.Printf(`
╔═══════════════════════════════════════════════════════════════╗
║                    Factory Dashboard                           ║
╠═══════════════════════════════════════════════════════════════╣
║  Server:   http://localhost:%s                              ║
║  Database: %s
╚═══════════════════════════════════════════════════════════════╝

`, port, dbPath)

	addr := ":" + port
	if err := server.Start(addr); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
