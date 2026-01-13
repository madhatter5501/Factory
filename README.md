# Factory - AI Development Pipeline Orchestrator

Factory is an AI development pipeline orchestrator that coordinates multiple AI agents (PM, Dev, QA, UX, Security) to autonomously develop software through a kanban-based workflow.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         Factory Orchestrator                     │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐ │
│  │   Kanban    │  │  Worktree   │  │    Agent Spawner        │ │
│  │   State     │  │  Manager    │  │  (CLI / API modes)      │ │
│  │  (SQLite)   │  │   (Git)     │  │                         │ │
│  └─────────────┘  └─────────────┘  └─────────────────────────┘ │
├─────────────────────────────────────────────────────────────────┤
│                         AI Agents                                │
│  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────────────┐│
│  │   PM   │ │  Dev   │ │   QA   │ │   UX   │ │    Security    ││
│  └────────┘ └────────┘ └────────┘ └────────┘ └────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

## Quick Start

```bash
# Build the factory
make build

# Start the web dashboard
./bin/factory --dashboard

# Run the orchestrator (processes tickets automatically)
./bin/factory --repo=/path/to/your/repo
```

## Components

### Orchestrator (`orchestrator.go`)
The core engine that manages the development pipeline:
- Monitors kanban board for tickets requiring work
- Spawns appropriate AI agents based on ticket domain
- Manages agent lifecycle (start, monitor, complete)
- Handles ticket state transitions

### State Store (`internal/db/`)
SQLite-backed persistence for kanban state:
- Tickets with full history tracking
- Agent runs and metrics
- Configuration storage
- Sub-ticket and parallel group management

### Web Dashboard (`internal/web/`)
HTTP server providing:
- Real-time kanban board view
- Ticket creation wizard
- Agent monitoring with SSE live updates
- Settings management

### Agent Spawner (`agents/`)
Dual-mode agent execution:
- **CLI Mode**: Spawns Claude CLI for each agent
- **API Mode**: Direct Anthropic API calls with prompt caching for efficiency

### Git Worktree Manager (`git/`)
Isolated development environments:
- Creates worktrees per ticket for parallel development
- Manages branches, rebasing, and merging
- Handles cleanup after ticket completion

## Kanban Workflow

```
BACKLOG → APPROVED → REFINING → READY → IN_DEV → IN_QA → IN_UX → IN_SEC → PM_REVIEW → DONE
                         ↓
                  NEEDS_EXPERT → AWAITING_USER
```

### Status Descriptions

| Status | Description |
|--------|-------------|
| `BACKLOG` | Ideas not yet approved for development |
| `APPROVED` | Approved, awaiting requirements analysis |
| `REFINING_ROUND_N` | PM analyzing with domain experts (collaborative PRD) |
| `NEEDS_EXPERT` | Requires specific domain expert consultation |
| `AWAITING_USER` | Blocked on user input/decision |
| `READY` | Requirements complete, ready for development |
| `IN_DEV` | Active development by dev agent |
| `IN_QA` | QA agent reviewing/testing |
| `IN_UX` | UX agent reviewing design/experience |
| `IN_SEC` | Security agent reviewing for vulnerabilities |
| `PM_REVIEW` | PM final review before completion |
| `DONE` | Ticket completed and merged |
| `BLOCKED` | Blocked by external dependency |

## Agent Types

### Product Management
- **PM**: Creates iterations, reviews tickets, final approval
- **PM-Requirements**: Analyzes and refines requirements
- **PM-Facilitator**: Coordinates collaborative PRD discussions
- **PM-Breakdown**: Breaks epics into sub-tickets

### Development
- **Dev-Frontend**: Web/UI development (Lit, Vue, React)
- **Dev-Backend**: API/service development (.NET, Go)
- **Dev-Infra**: Infrastructure/DevOps (Azure, Docker)

### Quality
- **QA**: Test planning and execution
- **UX**: Design review and accessibility
- **Security**: Vulnerability assessment

## CLI Options

```
Usage: factory [options]

Options:
  --repo           Repository root path (default: ".")
  --bare-repo      Bare repo path for local-only workflow
  --max-agents     Maximum parallel agents (default: 3)
  --timeout        Agent timeout (default: 30m)
  --interval       Cycle interval (default: 10s)
  --auto-merge     Auto-merge completed tickets
  --dry-run        Don't actually run agents
  --verbose        Verbose output (default: true)
  --version        Show version

  --init           Initialize a new kanban board
  --status         Show board status
  --dashboard      Start web dashboard only
  --with-dashboard Run agents with embedded dashboard
  --port           Dashboard port (default: 8080)
  --db             SQLite database path (default: factory.db)
```

## Configuration

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key (enables API mode) |

### Spawner Modes

- **CLI Mode**: Uses `claude` CLI tool (default when no API key)
- **API Mode**: Direct API calls with prompt caching (when API key present)
- **Auto Mode**: Automatically selects based on API key availability

## Directory Structure

```
agents/factory/
├── cmd/factory/          # CLI entry point
├── agents/               # Agent spawning (CLI/API)
│   └── rag/              # Retrieval-Augmented Generation
├── git/                  # Git worktree management
├── internal/
│   ├── db/               # SQLite storage
│   └── web/              # HTTP dashboard
│       ├── templates/    # HTML templates
│       └── static/       # CSS/JS assets
├── kanban/               # State types and interfaces
├── prompts/              # Agent prompt templates
│   └── experts/          # Domain expert prompts
└── docs/                 # Design documentation
```

## Development

```bash
# Build
make build

# Run tests
make test

# Format code
make fmt

# Lint (requires golangci-lint)
make lint

# Clean build artifacts
make clean
```

## Database Schema

The SQLite database (`factory.db`) contains:

- **tickets**: All kanban tickets with properties and state
- **ticket_history**: Status change history
- **agent_runs**: Record of all agent executions
- **config**: Key-value configuration storage

## Collaborative PRD Model

Factory supports multi-round collaborative PRD refinement:

1. User submits initial idea via web wizard
2. PM-Facilitator coordinates discussion
3. Domain experts (Backend, Frontend, Data, Security) provide input
4. Multiple rounds of refinement until consensus
5. Final PRD reviewed by user before implementation

See `docs/plans/collaborative-prd-model.md` for detailed design.

## License

Internal tool - not for external distribution.
