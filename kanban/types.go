// Package kanban provides state management for the AI development factory.
// It tracks tickets through the development pipeline using a JSON-based kanban board.
package kanban

import (
	"time"
)

// Status represents the current stage of a ticket in the pipeline.
type Status string

const (
	StatusBacklog      Status = "BACKLOG"       // Ideas, not yet planned
	StatusApproved     Status = "APPROVED"      // Approved in Notion, awaiting requirements
	StatusRefining     Status = "REFINING"      // PM analyzing, gathering requirements (legacy)
	StatusNeedsExpert  Status = "NEEDS_EXPERT"  // PM needs domain expert input (legacy)
	StatusAwaitingUser Status = "AWAITING_USER" // Requirements ready for user review/edit
	StatusReady        Status = "READY"         // Requirements complete, ready for dev
	StatusInDev        Status = "IN_DEV"        // Developer agent is working on it
	StatusInQA         Status = "IN_QA"         // QA agent is testing
	StatusInUX         Status = "IN_UX"         // UX agent is reviewing
	StatusInSec        Status = "IN_SEC"        // Security agent is reviewing
	StatusPMReview     Status = "PM_REVIEW"     // PM agent verifies expected behavior
	StatusDone         Status = "DONE"          // Complete, merged to main
	StatusBlocked      Status = "BLOCKED"       // Blocked by bugs or dependencies

	// Collaborative PRD refinement statuses
	StatusRefiningRound Status = "REFINING_ROUND" // PM facilitating multi-round discussion (append round number)
	StatusPRDComplete   Status = "PRD_COMPLETE"   // All experts reached consensus, PRD finalized
	StatusBreakingDown  Status = "BREAKING_DOWN"  // PM creating sub-tickets from PRD
)

// Domain represents the area of the codebase a ticket affects.
type Domain string

const (
	DomainFrontend Domain = "frontend" // Lit, Vue, UI components
	DomainBackend  Domain = "backend"  // .NET, APIs, services
	DomainInfra    Domain = "infra"    // K8s, Azure, deployment
	DomainDatabase Domain = "database" // Migrations, queries
	DomainShared   Domain = "shared"   // Cross-cutting concerns
)

// Priority determines the order tickets are worked on.
type Priority int

const (
	PriorityCritical Priority = 1
	PriorityHigh     Priority = 2
	PriorityMedium   Priority = 3
	PriorityLow      Priority = 4
)

// Signoffs tracks which agents have approved the ticket.
type Signoffs struct {
	Dev      bool   `json:"dev"`
	DevAgent string `json:"devAgent,omitempty"` // Which dev agent signed off
	DevAt    string `json:"devAt,omitempty"`
	QA       bool   `json:"qa"`
	QAAt     string `json:"qaAt,omitempty"`
	UX       bool   `json:"ux"`
	UXAt     string `json:"uxAt,omitempty"`
	Security bool   `json:"security"`
	SecAt    string `json:"secAt,omitempty"`
	PM       bool   `json:"pm"`
	PMAt     string `json:"pmAt,omitempty"`
}

// Bug represents an issue found during QA or review.
type Bug struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Severity    string    `json:"severity"` // critical, high, medium, low
	FoundBy     string    `json:"foundBy"`  // qa, ux, security
	FoundAt     time.Time `json:"foundAt"`
	Fixed       bool      `json:"fixed"`
	FixedAt     string    `json:"fixedAt,omitempty"`
}

// ExpertConsultation tracks a domain expert's input during requirements refinement.
// DEPRECATED: Use PRDConversation for collaborative multi-round discussions.
type ExpertConsultation struct {
	Domain    Domain    `json:"domain"`    // Which domain expert was consulted
	Question  string    `json:"question"`  // PM's question
	Response  string    `json:"response"`  // Expert's answer
	AskedAt   time.Time `json:"askedAt"`
	AnsweredAt time.Time `json:"answeredAt,omitempty"`
}

// ExpertInput captures a single expert's response in a PRD discussion round.
type ExpertInput struct {
	Agent             string   `json:"agent"`             // "dev", "qa", "ux", "security"
	Response          string   `json:"response"`          // Full response text
	KeyPoints         []string `json:"keyPoints"`         // Extracted key points
	Concerns          []string `json:"concerns"`          // Any concerns raised
	QuestionsForOthers []string `json:"questionsForOthers"` // Questions for other experts
	Approves          bool     `json:"approves"`          // Ready to proceed?
	Reasoning         string   `json:"reasoning"`         // Why they approve or what's blocking
}

// ConversationRound represents one round of the PM-facilitated PRD discussion.
type ConversationRound struct {
	RoundNumber  int                    `json:"roundNumber"`
	PMPrompt     string                 `json:"pmPrompt"`     // What PM asked this round
	ExpertInputs map[string]ExpertInput `json:"expertInputs"` // Responses from each expert (keyed by agent name)
	PMSynthesis  string                 `json:"pmSynthesis"`  // PM's summary after round
	Timestamp    time.Time              `json:"timestamp"`
}

// PRDConversation tracks the full collaborative PRD development process.
type PRDConversation struct {
	TicketID      string              `json:"ticketId"`
	Rounds        []ConversationRound `json:"rounds"`
	CurrentRound  int                 `json:"currentRound"`        // Which round we're currently in
	Status        string              `json:"status"`              // "in_progress", "consensus", "blocked", "awaiting_user"
	FinalPRD      string              `json:"finalPrd,omitempty"`  // Final PRD document after consensus
	SubTicketIDs  []string            `json:"subTicketIds,omitempty"` // Created sub-ticket IDs
	UserQuestions []Question          `json:"userQuestions,omitempty"` // Questions for user during discussion
	StartedAt     time.Time           `json:"startedAt"`
	CompletedAt   time.Time           `json:"completedAt,omitempty"`
}

// Question represents a PM question and its answer.
type Question struct {
	Question string `json:"question"`         // The question from PM
	Answer   string `json:"answer,omitempty"` // User's answer
}

// Requirements tracks the refinement process for a ticket.
type Requirements struct {
	// Analysis
	PMAnalysis      string     `json:"pmAnalysis,omitempty"`      // PM's initial analysis
	Questions       []Question `json:"questions,omitempty"`       // Open questions that need answers
	TechnicalNotes  string     `json:"technicalNotes,omitempty"`  // Technical considerations

	// Expert consultations
	Consultations []ExpertConsultation `json:"consultations,omitempty"`

	// Final requirements (compiled from original + expert input)
	FinalDescription string   `json:"finalDescription,omitempty"` // Refined description
	FinalCriteria    []string `json:"finalCriteria,omitempty"`    // Refined acceptance criteria

	// User review
	UserEdits       string    `json:"userEdits,omitempty"`       // Notes from user edits
	UserApprovedAt  time.Time `json:"userApprovedAt,omitempty"`  // When user approved requirements
	UserApprovedBy  string    `json:"userApprovedBy,omitempty"`  // Who approved (from Notion)

	// Timestamps
	StartedAt   time.Time `json:"startedAt,omitempty"`
	CompletedAt time.Time `json:"completedAt,omitempty"`
}

// HistoryEntry tracks state transitions.
type HistoryEntry struct {
	Status    Status    `json:"status"`
	At        time.Time `json:"at"`
	By        string    `json:"by"`     // Agent that made the change
	Note      string    `json:"note,omitempty"`
}

// Worktree tracks the git worktree for this ticket.
type Worktree struct {
	Path   string `json:"path"`   // .worktrees/TICKET-001-description
	Branch string `json:"branch"` // feat/TICKET-001-description
	Active bool   `json:"active"` // Is worktree currently in use
}

// Ticket represents a single unit of work in the pipeline.
type Ticket struct {
	// Identity
	ID          string `json:"id"`          // TICKET-001
	Title       string `json:"title"`       // Short descriptive title
	Description string `json:"description"` // Full description with context

	// Classification
	Domain   Domain   `json:"domain"`   // frontend, backend, infra, database
	Priority Priority `json:"priority"` // 1-4, lower is higher priority
	Type     string   `json:"type"`     // feature, bugfix, tech-debt, security

	// Scope (for conflict detection)
	Files        []string `json:"files"`        // Glob patterns: ["src/api/*", "src/models/user.*"]
	Dependencies []string `json:"dependencies"` // Ticket IDs this depends on

	// Acceptance criteria
	AcceptanceCriteria []string `json:"acceptanceCriteria"`

	// Requirements refinement (populated during REFINING phase)
	Requirements *Requirements `json:"requirements,omitempty"`

	// Collaborative PRD discussion (populated during REFINING_ROUND phases)
	Conversation *PRDConversation `json:"conversation,omitempty"`

	// Parent-child relationship (for PRD breakdown into sub-tickets)
	ParentID      string `json:"parentId,omitempty"`      // Parent PRD ticket ID (for sub-tickets)
	ParallelGroup int    `json:"parallelGroup,omitempty"` // Group number for parallel execution scheduling

	// Pipeline state
	Status          Status   `json:"status"`
	AssignedAgent   string   `json:"assignedAgent,omitempty"` // dev-frontend, dev-backend, etc.
	Assignee        string   `json:"assignee,omitempty"`      // Short agent name for display
	CurrentActivity string   `json:"currentActivity,omitempty"` // What the agent is currently doing
	Signoffs        Signoffs `json:"signoffs"`
	Bugs            []Bug    `json:"bugs,omitempty"`

	// Git integration
	Worktree *Worktree `json:"worktree,omitempty"`

	// Tracking
	History   []HistoryEntry `json:"history"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`

	// Agent notes (learnings, context for future agents)
	Notes string `json:"notes,omitempty"`
}

// Iteration represents a sprint/iteration of work.
type Iteration struct {
	ID        string    `json:"id"`        // 2024-01-sprint-3
	Goal      string    `json:"goal"`      // High-level iteration goal
	CreatedBy string    `json:"createdBy"` // pm-agent
	CreatedAt time.Time `json:"createdAt"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	Status    string    `json:"status"` // planning, active, complete
}

// AgentRun tracks a single agent execution.
type AgentRun struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`     // pm, dev-frontend, qa, etc.
	TicketID  string    `json:"ticketId"`
	Worktree  string    `json:"worktree"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	Status    string    `json:"status"` // running, success, failed
	Output    string    `json:"output,omitempty"`
}

// Board is the top-level kanban state.
type Board struct {
	// Metadata
	Version   string    `json:"version"` // Schema version
	UpdatedAt time.Time `json:"updatedAt"`

	// Current iteration
	Iteration *Iteration `json:"iteration,omitempty"`

	// All tickets
	Tickets []Ticket `json:"tickets"`

	// Active agent runs
	ActiveRuns []AgentRun `json:"activeRuns,omitempty"`

	// Configuration
	Config BoardConfig `json:"config"`
}

// BoardConfig holds factory configuration.
type BoardConfig struct {
	// Worktree settings
	WorktreeDir string `json:"worktreeDir"` // .worktrees

	// Agent settings
	MaxParallelAgents  int `json:"maxParallelAgents"`  // Max concurrent agents
	MaxTicketsPerAgent int `json:"maxTicketsPerAgent"` // Max tickets one agent handles

	// Git settings
	MainBranch     string `json:"mainBranch"`     // main
	BranchPrefix   string `json:"branchPrefix"`   // feat/
	SquashOnMerge  bool   `json:"squashOnMerge"`  // Use squash merge
	RebaseOnUpdate bool   `json:"rebaseOnUpdate"` // Rebase to keep updated

	// Pipeline settings
	RequireAllSignoffs bool     `json:"requireAllSignoffs"` // All agents must sign off
	SkipStages         []Status `json:"skipStages"`         // Stages to skip (e.g., UX for backend-only)
}

// NewBoard creates a new kanban board with sensible defaults.
func NewBoard() *Board {
	return &Board{
		Version:   "1.0.0",
		UpdatedAt: time.Now(),
		Tickets:   []Ticket{},
		Config: BoardConfig{
			WorktreeDir:        ".worktrees",
			MaxParallelAgents:  3,
			MaxTicketsPerAgent: 1,
			MainBranch:         "main",
			BranchPrefix:       "feat/",
			SquashOnMerge:      true,
			RebaseOnUpdate:     true,
			RequireAllSignoffs: true,
			SkipStages:         []Status{},
		},
	}
}
