package kanban

import "time"

// StateStore is the interface for kanban state storage.
// Both JSON file-based State and SQLite Store implement this interface.
type StateStore interface {
	// Lifecycle
	Load() error
	Save() error

	// Board
	GetBoard() Board
	GetConfig() BoardConfig

	// Ticket queries
	GetTicket(id string) (*Ticket, bool)
	GetAllTickets() ([]Ticket, error)
	GetTicketsByStatus(status Status) []Ticket
	GetTicketsByDomain(domain Domain) []Ticket
	GetTicketsByParent(parentID string) []Ticket
	GetReadyTickets() []Ticket
	GetNextTicketForDomain(domain Domain) (*Ticket, bool)
	GetInProgressCount() int
	GetStats() map[Status]int

	// Ticket mutations
	AddTicket(t Ticket) error
	CreateTicket(t *Ticket) error
	UpdateTicketStatus(id string, newStatus Status, by string, note string) error
	AssignAgent(ticketID, agentID string) error
	SetWorktree(ticketID string, wt *Worktree) error
	AddSignoff(ticketID string, stage string, agentID string) error
	AddBug(ticketID string, bug Bug) error
	UpdateNotes(ticketID, notes string) error
	UpdateActivity(ticketID, activity, assignee string) error
	ClearActivity(ticketID string) error
	UpdateTicket(ticket *Ticket) error

	// Iteration
	SetIteration(iter *Iteration)
	GetIteration() *Iteration
	IsIterationComplete() bool

	// Active runs
	AddRun(run *AgentRun) error
	AddActiveRun(run AgentRun)
	CompleteRun(runID string, status string, output string)
	GetActiveRuns() []AgentRun
	GetActiveDevRuns() []AgentRun
	GetActiveRunsForTicket(ticketID string) []AgentRun
	CleanupStaleRuns(maxAge time.Duration)
	CleanupStaleRunningAgents(maxRunDuration time.Duration) int
	CleanupOrphanedRunningAgents() int // Mark ALL running agents as failed on startup
	IsAgentRunning(ticketID, agentType string) bool

	// Conversations
	CreateConversation(conv *TicketConversation) error
	AddConversationMessage(msg *ConversationMessage) error
}
