// Package kanban provides state management for the AI development factory.
// It tracks tickets through the development pipeline using a JSON-based kanban board.
package kanban

import (
	"fmt"
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
	By        string    `json:"by"` // Agent that made the change
	Note      string    `json:"note,omitempty"`
}

// TimeStats holds computed timing statistics for a ticket.
type TimeStats struct {
	TotalWorkTime    time.Duration            `json:"totalWorkTime"`    // Total time agents actively worked
	TotalIdleTime    time.Duration            `json:"totalIdleTime"`    // Total time ticket sat waiting
	TotalCycleTime   time.Duration            `json:"totalCycleTime"`   // Total time from creation to completion
	StatusDurations  map[Status]time.Duration `json:"statusDurations"`  // Time spent in each status
	AgentWorkTimes   map[string]time.Duration `json:"agentWorkTimes"`   // Work time per agent type
	LastActivityAt   time.Time                `json:"lastActivityAt"`   // When last agent activity occurred
	IdleSince        time.Time                `json:"idleSince"`        // When ticket became idle (if currently idle)
	CurrentIdleTime  time.Duration            `json:"currentIdleTime"`  // How long it's been idle
	AgentRunCount    int                      `json:"agentRunCount"`    // Number of agent runs
}

// Worktree tracks the git worktree for this ticket.
type Worktree struct {
	Path   string `json:"path"`   // .worktrees/TICKET-001-description
	Branch string `json:"branch"` // feat/TICKET-001-description
	Active bool   `json:"active"` // Is worktree currently in use
	Merged bool   `json:"merged"` // Has the branch been merged to main
}

// TechnicalContext provides technical details for developer agents.
type TechnicalContext struct {
	Stack            []string `json:"stack"`            // Technologies: ["lit", "typescript", "vite"]
	AffectedPaths    []string `json:"affectedPaths"`    // Paths to modify: ["packages/web/components/"]
	PatternsToFollow []string `json:"patternsToFollow"` // Example files: ["packages/web/components/button.ts"]
}

// Constraints defines what the implementation must avoid or ensure.
type Constraints struct {
	MustNot       []string `json:"mustNot"`       // Things to avoid: ["break existing API"]
	Security      []string `json:"security"`      // Security requirements: ["input sanitization"]
	Accessibility string   `json:"accessibility"` // A11y requirements: "WCAG 2.1 AA"
	Performance   string   `json:"performance"`   // Perf constraints: "< 100ms render"
}

// BlockedReason explains why a ticket is blocked (for human supervisors).
type BlockedReason struct {
	Category    string `json:"category"`    // dependency, bug, policy, confidence, ambiguous
	Summary     string `json:"summary"`     // One-sentence human explanation
	TicketID    string `json:"ticketId,omitempty"`    // Related blocking ticket if dependency
	IsManaged   bool   `json:"isManaged"`   // Is the system actively addressing this?
}

// CreationContext explains why a ticket was created (for human supervisors).
type CreationContext struct {
	Reason      string `json:"reason"`      // prd_breakdown, detected_issue, dependency, user_request
	ParentTitle string `json:"parentTitle,omitempty"` // Title of parent ticket if from breakdown
	Details     string `json:"details,omitempty"`     // Additional context
}

// SystemHealthStatus represents the overall health of the factory.
type SystemHealthStatus string

const (
	SystemHealthStable      SystemHealthStatus = "stable"      // Normal operation
	SystemHealthThrashing   SystemHealthStatus = "thrashing"   // Work cycling without progress
	SystemHealthReworking   SystemHealthStatus = "reworking"   // High rejection/rework rate
	SystemHealthAccumulating SystemHealthStatus = "accumulating" // Blocked work piling up
	SystemHealthStalled     SystemHealthStatus = "stalled"     // No progress being made
)

// SystemHealth provides a summary of factory health for human supervisors.
type SystemHealth struct {
	Status           SystemHealthStatus `json:"status"`
	StatusLabel      string             `json:"statusLabel"`      // Human-readable status
	Message          string             `json:"message"`          // Explanation of current state
	BlockedCount     int                `json:"blockedCount"`
	ActiveCount      int                `json:"activeCount"`
	BlockedRatio     float64            `json:"blockedRatio"`     // Blocked / (Active + Blocked)
	AvgIdleTime      time.Duration      `json:"avgIdleTime"`      // Average time tickets sit idle
	ReworkRate       float64            `json:"reworkRate"`       // % of tickets that went backwards
	ThrashingTickets []string           `json:"thrashingTickets"` // Ticket IDs that keep cycling
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

	// Technical context for developer agents
	TechnicalContext *TechnicalContext `json:"technicalContext,omitempty"`
	Constraints      *Constraints      `json:"constraints,omitempty"`

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

	// Tags (populated on fetch from junction table)
	Tags []Tag `json:"tags,omitempty"`

	// Human supervisor context (computed/populated for UI display)
	BlockedReason    *BlockedReason   `json:"blockedReason,omitempty"`    // Why this is blocked
	CreationContext  *CreationContext `json:"creationContext,omitempty"` // Why this was created
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

// Duration returns the duration of the agent run.
// Returns 0 if the run hasn't ended yet.
func (r AgentRun) Duration() time.Duration {
	if r.EndedAt.IsZero() {
		return 0
	}
	return r.EndedAt.Sub(r.StartedAt)
}

// AuditEventType represents the type of audit event.
type AuditEventType string

const (
	AuditEventPromptSent      AuditEventType = "prompt_sent"
	AuditEventResponseReceived AuditEventType = "response_received"
	AuditEventToolCall        AuditEventType = "tool_call"
	AuditEventError           AuditEventType = "error"
)

// AuditEntry represents a single audit log entry capturing agent interactions.
type AuditEntry struct {
	ID          string         `json:"id"`
	RunID       string         `json:"runId,omitempty"`       // Reference to AgentRun
	TicketID    string         `json:"ticketId"`
	Agent       string         `json:"agent"`
	EventType   AuditEventType `json:"eventType"`
	EventData   string         `json:"eventData,omitempty"`   // JSON: prompt, response, tool args, etc.
	TokenInput  int            `json:"tokenInput,omitempty"`
	TokenOutput int            `json:"tokenOutput,omitempty"`
	DurationMs  int            `json:"durationMs,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// ThreadType represents the type of conversation thread.
type ThreadType string

const (
	ThreadTypeDevDiscussion ThreadType = "dev_discussion"
	ThreadTypeQAFeedback    ThreadType = "qa_feedback"
	ThreadTypePMCheckin     ThreadType = "pm_checkin"
	ThreadTypeBlocker       ThreadType = "blocker"
	ThreadTypeUserQuestion  ThreadType = "user_question"

	// Sign-off report thread types - created when agents complete reviews
	ThreadTypeDevSignoff      ThreadType = "dev_signoff"
	ThreadTypeQASignoff       ThreadType = "qa_signoff"
	ThreadTypeUXSignoff       ThreadType = "ux_signoff"
	ThreadTypeSecuritySignoff ThreadType = "security_signoff"
	ThreadTypePMSignoff       ThreadType = "pm_signoff"
)

// ThreadStatus represents the status of a conversation thread.
type ThreadStatus string

const (
	ThreadStatusOpen      ThreadStatus = "open"
	ThreadStatusResolved  ThreadStatus = "resolved"
	ThreadStatusEscalated ThreadStatus = "escalated"
)

// TicketConversation represents a threaded conversation about a ticket during development.
type TicketConversation struct {
	ID         string       `json:"id"`
	TicketID   string       `json:"ticketId"`
	ThreadType ThreadType   `json:"threadType"`
	Title      string       `json:"title,omitempty"`
	Status     ThreadStatus `json:"status"`
	CreatedAt  time.Time    `json:"createdAt"`
	ResolvedAt time.Time    `json:"resolvedAt,omitempty"`
	Messages   []ConversationMessage `json:"messages,omitempty"` // Populated on fetch
}

// MessageType represents the type of conversation message.
type MessageType string

const (
	MessageTypeQuestion     MessageType = "question"
	MessageTypeResponse     MessageType = "response"
	MessageTypeDecision     MessageType = "decision"
	MessageTypeStatusUpdate MessageType = "status_update"
	MessageTypeBlocker      MessageType = "blocker"
	MessageTypeSignoffReport MessageType = "signoff_report" // Structured review findings
)

// ConversationMessage represents a single message in a conversation thread.
type ConversationMessage struct {
	ID             string       `json:"id"`
	ConversationID string       `json:"conversationId"`
	Agent          string       `json:"agent"` // pm, dev-frontend, qa, user, etc.
	MessageType    MessageType  `json:"messageType"`
	Content        string       `json:"content"`
	Metadata       string       `json:"metadata,omitempty"` // JSON: additional context
	Attachments    []Attachment `json:"attachments,omitempty"` // Populated on fetch
	CreatedAt      time.Time    `json:"createdAt"`
}

// Attachment represents a file attached to a conversation message.
type Attachment struct {
	ID          string    `json:"id"`
	MessageID   string    `json:"messageId"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"contentType"`
	Size        int64     `json:"size"`
	Path        string    `json:"path"` // Filesystem path
	CreatedAt   time.Time `json:"createdAt"`
}

// SignoffReport represents parsed agent review output for sign-off documentation.
type SignoffReport struct {
	Status           string            `json:"status"` // passed, failed
	Agent            string            `json:"agent"`
	TicketID         string            `json:"ticket_id"`
	Summary          string            `json:"summary,omitempty"`
	ChecksPerformed  []string          `json:"checks_performed,omitempty"`
	CriteriaVerified []string          `json:"criteria_verified,omitempty"`
	TestsRun         *TestRunResult    `json:"tests_run,omitempty"`
	Findings         []SignoffFinding  `json:"findings,omitempty"`
	Bugs             []Bug             `json:"bugs,omitempty"`
	UnmetCriteria    []string          `json:"unmet_criteria,omitempty"`
	Notes            string            `json:"notes,omitempty"`
	Reason           string            `json:"reason,omitempty"` // For failures
}

// TestRunResult holds test execution statistics.
type TestRunResult struct {
	Framework string `json:"framework"`
	Passed    int    `json:"passed"`
	Failed    int    `json:"failed"`
	Skipped   int    `json:"skipped,omitempty"`
}

// SignoffFinding represents an issue found during review.
type SignoffFinding struct {
	ID             string `json:"id,omitempty"`
	Severity       string `json:"severity"` // critical, high, medium, low
	Category       string `json:"category,omitempty"` // For UX: accessibility, usability, consistency
	Title          string `json:"title,omitempty"`
	Description    string `json:"description"`
	File           string `json:"file,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
	// For bugs/QA
	StepsToReproduce string `json:"steps_to_reproduce,omitempty"`
	Expected         string `json:"expected,omitempty"`
	Actual           string `json:"actual,omitempty"`
}

// CheckinType represents the type of PM check-in.
type CheckinType string

const (
	CheckinTypeProgress CheckinType = "progress"
	CheckinTypeBlocker  CheckinType = "blocker"
	CheckinTypeGuidance CheckinType = "guidance"
	CheckinTypeReview   CheckinType = "review"
)

// PMCheckin represents a PM agent check-in during development.
type PMCheckin struct {
	ID             string             `json:"id"`
	TicketID       string             `json:"ticketId"`
	ConversationID string             `json:"conversationId,omitempty"` // Links to thread if created
	CheckinType    CheckinType        `json:"checkinType"`
	Summary        string             `json:"summary"`
	Findings       *PMCheckinFindings `json:"findings,omitempty"` // Structured findings from check-in
	ActionRequired string             `json:"actionRequired,omitempty"` // What needs to happen
	Resolved       bool               `json:"resolved"`
	CreatedAt      time.Time          `json:"createdAt"`
}

// PMCheckinFindings represents the structured findings from a PM check-in.
type PMCheckinFindings struct {
	ProgressPercent int      `json:"progressPercent,omitempty"`
	Concerns        []string `json:"concerns,omitempty"`
	Blockers        []string `json:"blockers,omitempty"`
	Achievements    []string `json:"achievements,omitempty"`
}

// WorktreePoolStatus represents the status of a worktree in the pool.
type WorktreePoolStatus string

const (
	WorktreePoolStatusActive         WorktreePoolStatus = "active"
	WorktreePoolStatusMerging        WorktreePoolStatus = "merging"
	WorktreePoolStatusCleanupPending WorktreePoolStatus = "cleanup_pending"
)

// WorktreePoolEntry represents a tracked worktree in the global pool.
type WorktreePoolEntry struct {
	ID           string             `json:"id"`
	TicketID     string             `json:"ticketId"`
	Branch       string             `json:"branch"`
	Path         string             `json:"path"`
	Agent        string             `json:"agent"`
	Status       WorktreePoolStatus `json:"status"`
	CreatedAt    time.Time          `json:"createdAt"`
	LastActivity time.Time          `json:"lastActivity"`
}

// MergeQueueStatus represents the status of a merge operation.
type MergeQueueStatus string

const (
	MergeQueueStatusPending    MergeQueueStatus = "pending"
	MergeQueueStatusInProgress MergeQueueStatus = "in_progress"
	MergeQueueStatusCompleted  MergeQueueStatus = "completed"
	MergeQueueStatusFailed     MergeQueueStatus = "failed"
)

// MergeQueueEntry represents a pending or completed merge operation.
type MergeQueueEntry struct {
	ID          string           `json:"id"`
	TicketID    string           `json:"ticketId"`
	Branch      string           `json:"branch"`
	Status      MergeQueueStatus `json:"status"`
	Attempts    int              `json:"attempts"`
	LastError   string           `json:"lastError,omitempty"`
	CreatedAt   time.Time        `json:"createdAt"`
	CompletedAt *time.Time       `json:"completedAt,omitempty"`
}

// WorktreeEventType represents the type of worktree lifecycle event.
type WorktreeEventType string

const (
	WorktreeEventCreated        WorktreeEventType = "created"
	WorktreeEventMergeStarted   WorktreeEventType = "merge_started"
	WorktreeEventMergeCompleted WorktreeEventType = "merge_completed"
	WorktreeEventMergeFailed    WorktreeEventType = "merge_failed"
	WorktreeEventCleanedUp      WorktreeEventType = "cleaned_up"
	WorktreeEventLimitEnforced  WorktreeEventType = "limit_enforced"
)

// WorktreeEvent represents a worktree lifecycle event for auditing.
type WorktreeEvent struct {
	ID        string            `json:"id"`
	TicketID  string            `json:"ticketId"`
	EventType WorktreeEventType `json:"eventType"`
	EventData string            `json:"eventData,omitempty"` // JSON: additional context
	CreatedAt time.Time         `json:"createdAt"`
}

// WorktreePoolStats provides statistics about the worktree pool.
type WorktreePoolStats struct {
	ActiveCount   int `json:"activeCount"`
	MergingCount  int `json:"mergingCount"`
	PendingCount  int `json:"pendingCount"` // Tickets waiting for worktree slot
	Limit         int `json:"limit"`
	AvailableSlots int `json:"availableSlots"`
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

// ADRStatus represents the status of an Architecture Decision Record.
type ADRStatus string

const (
	ADRStatusProposed   ADRStatus = "proposed"
	ADRStatusAccepted   ADRStatus = "accepted"
	ADRStatusDeprecated ADRStatus = "deprecated"
	ADRStatusSuperseded ADRStatus = "superseded"
)

// ADR represents an Architecture Decision Record captured during requirement gathering.
type ADR struct {
	ID           string    `json:"id"`           // "ADR-001"
	Title        string    `json:"title"`        // "Use WebSocket for real-time updates"
	Status       ADRStatus `json:"status"`       // proposed, accepted, deprecated, superseded
	Context      string    `json:"context"`      // Why the decision was needed
	Decision     string    `json:"decision"`     // What was decided
	Consequences string    `json:"consequences"` // Trade-offs and implications
	IterationID  string    `json:"iterationId"`  // Which iteration it was created in
	TicketIDs    []string  `json:"ticketIds"`    // Related tickets (populated on fetch)
	SupersededBy string    `json:"supersededBy"` // ID of ADR that supersedes this
	CreatedBy    string    `json:"createdBy"`    // "pm-agent"
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// TagType represents the category of a tag.
type TagType string

const (
	TagTypeEpic       TagType = "epic"
	TagTypeTheme      TagType = "theme"
	TagTypeComponent  TagType = "component"
	TagTypeInitiative TagType = "initiative"
	TagTypeGeneric    TagType = "tag"
)

// Tag represents a flexible tag that can categorize tickets.
// Tags support N:M relationships - a ticket can have multiple epic tags.
type Tag struct {
	ID          string  `json:"id"`          // UUID
	Name        string  `json:"name"`        // "Auth Refactor"
	Type        TagType `json:"type"`        // "epic", "theme", "component", "initiative", "tag"
	Color       string  `json:"color"`       // CSS color for UI display (e.g., "#6366f1")
	Description string  `json:"description"` // Optional description
}

// FormatADRID formats an ADR number into a standard ADR ID string (e.g., "ADR-001").
func FormatADRID(num int) string {
	return fmt.Sprintf("ADR-%03d", num)
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

// ComputeBlockedReason analyzes a blocked ticket and returns a human-readable explanation.
func (t *Ticket) ComputeBlockedReason(allTickets []Ticket) *BlockedReason {
	if t.Status != StatusBlocked {
		return nil
	}

	// Check for blocking bugs
	var criticalBugs, highBugs int
	for _, bug := range t.Bugs {
		if !bug.Fixed {
			switch bug.Severity {
			case "critical":
				criticalBugs++
			case "high":
				highBugs++
			}
		}
	}
	if criticalBugs > 0 {
		return &BlockedReason{
			Category:  "bug",
			Summary:   fmt.Sprintf("Blocked by %d critical bug(s) requiring fixes", criticalBugs),
			IsManaged: true, // System will route back to dev
		}
	}
	if highBugs > 0 {
		return &BlockedReason{
			Category:  "bug",
			Summary:   fmt.Sprintf("Blocked by %d high-severity bug(s)", highBugs),
			IsManaged: true,
		}
	}

	// Check for blocking dependencies
	for _, depID := range t.Dependencies {
		for _, other := range allTickets {
			if other.ID == depID && other.Status != StatusDone {
				return &BlockedReason{
					Category:  "dependency",
					Summary:   fmt.Sprintf("Waiting on: %s", other.Title),
					TicketID:  depID,
					IsManaged: other.Status != StatusBlocked, // Managed unless dependency is also blocked
				}
			}
		}
	}

	// Check history for blocking reason
	for i := len(t.History) - 1; i >= 0; i-- {
		entry := t.History[i]
		if entry.Status == StatusBlocked && entry.Note != "" {
			isManaged := true
			category := "issue"

			// Categorize based on note content
			note := entry.Note
			switch {
			case contains(note, "security"):
				category = "policy"
			case contains(note, "confidence") || contains(note, "unclear"):
				category = "confidence"
				isManaged = false // Needs human guidance
			case contains(note, "ambiguous") || contains(note, "requirement"):
				category = "ambiguous"
				isManaged = false
			}

			return &BlockedReason{
				Category:  category,
				Summary:   truncateString(entry.Note, 80),
				IsManaged: isManaged,
			}
		}
	}

	// Default fallback
	return &BlockedReason{
		Category:  "unknown",
		Summary:   "Blocked (reason not recorded)",
		IsManaged: false,
	}
}

// ComputeCreationContext returns context about why this ticket was created.
func (t *Ticket) ComputeCreationContext(allTickets []Ticket) *CreationContext {
	// If it has a parent, it came from PRD breakdown
	if t.ParentID != "" {
		var parentTitle string
		for _, other := range allTickets {
			if other.ID == t.ParentID {
				parentTitle = other.Title
				break
			}
		}
		return &CreationContext{
			Reason:      "prd_breakdown",
			ParentTitle: parentTitle,
			Details:     "Created from PRD decomposition",
		}
	}

	// Based on type
	switch t.Type {
	case "bugfix":
		return &CreationContext{
			Reason:  "detected_issue",
			Details: "Created to address a detected bug",
		}
	case "tech-debt":
		return &CreationContext{
			Reason:  "detected_issue",
			Details: "Created to address technical debt",
		}
	case "security":
		return &CreationContext{
			Reason:  "detected_issue",
			Details: "Created to address security concern",
		}
	}

	// Default - user requested feature
	return &CreationContext{
		Reason:  "user_request",
		Details: "User-requested work",
	}
}

// ComputeSystemHealth analyzes the board state and returns health indicators.
func ComputeSystemHealth(tickets []Ticket) *SystemHealth {
	var blocked, active, done, reworked, thrashing int
	var totalIdleTime time.Duration
	var idleCount int
	thrashingTickets := []string{}

	for _, t := range tickets {
		switch t.Status {
		case StatusBlocked:
			blocked++
		case StatusDone:
			done++
		case StatusInDev, StatusInQA, StatusInUX, StatusInSec, StatusPMReview:
			active++
		}

		// Count rework (tickets that went backwards in the pipeline)
		reworked += countRework(t.History)

		// Detect thrashing (same status appearing 3+ times)
		if isThrashing(t.History) {
			thrashing++
			thrashingTickets = append(thrashingTickets, t.ID)
		}
	}

	total := blocked + active
	if total == 0 {
		return &SystemHealth{
			Status:       SystemHealthStable,
			StatusLabel:  "Stable",
			Message:      "No active work in progress",
			BlockedCount: 0,
			ActiveCount:  0,
		}
	}

	blockedRatio := float64(blocked) / float64(total)
	reworkRate := 0.0
	if len(tickets) > 0 {
		reworkRate = float64(reworked) / float64(len(tickets))
	}

	avgIdle := time.Duration(0)
	if idleCount > 0 {
		avgIdle = totalIdleTime / time.Duration(idleCount)
	}

	health := &SystemHealth{
		BlockedCount:     blocked,
		ActiveCount:      active,
		BlockedRatio:     blockedRatio,
		ReworkRate:       reworkRate,
		AvgIdleTime:      avgIdle,
		ThrashingTickets: thrashingTickets,
	}

	// Determine status based on metrics
	switch {
	case thrashing >= 3:
		health.Status = SystemHealthThrashing
		health.StatusLabel = "Thrashing"
		health.Message = fmt.Sprintf("%d tickets cycling without progress", thrashing)
	case reworkRate > 0.3:
		health.Status = SystemHealthReworking
		health.StatusLabel = "Reworking"
		health.Message = "High rejection rate - reviews finding issues"
	case blockedRatio > 0.5:
		health.Status = SystemHealthAccumulating
		health.StatusLabel = "Accumulating Debt"
		health.Message = fmt.Sprintf("%d blocked vs %d active - blockers piling up", blocked, active)
	case active == 0 && blocked > 0:
		health.Status = SystemHealthStalled
		health.StatusLabel = "Stalled"
		health.Message = "All work is blocked - intervention may be needed"
	default:
		health.Status = SystemHealthStable
		health.StatusLabel = "Stable"
		health.Message = fmt.Sprintf("%d active, %d blocked - normal operation", active, blocked)
	}

	return health
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsLower(s, substr))
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFoldSubstr(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func equalFoldSubstr(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func countRework(history []HistoryEntry) int {
	rework := 0
	statusOrder := map[Status]int{
		StatusBacklog:      0,
		StatusApproved:     1,
		StatusRefining:     2,
		StatusAwaitingUser: 3,
		StatusReady:        4,
		StatusInDev:        5,
		StatusInQA:         6,
		StatusInUX:         7,
		StatusInSec:        8,
		StatusPMReview:     9,
		StatusDone:         10,
	}

	var prevOrder int
	for i, entry := range history {
		order, ok := statusOrder[entry.Status]
		if !ok {
			continue
		}
		if i > 0 && order < prevOrder {
			rework++
		}
		prevOrder = order
	}
	return rework
}

func isThrashing(history []HistoryEntry) bool {
	if len(history) < 6 {
		return false
	}

	// Count occurrences of each status in recent history
	statusCounts := make(map[Status]int)
	recentHistory := history
	if len(history) > 10 {
		recentHistory = history[len(history)-10:]
	}

	for _, entry := range recentHistory {
		statusCounts[entry.Status]++
	}

	// If any status appears 3+ times in recent history, it's thrashing
	for _, count := range statusCounts {
		if count >= 3 {
			return true
		}
	}
	return false
}
