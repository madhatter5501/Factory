package kanban

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// State manages the kanban board state with file persistence.
type State struct {
	mu       sync.RWMutex
	board    *Board
	filePath string
	dirty    bool
}

// NewState creates a new state manager.
func NewState(filePath string) *State {
	return &State{
		filePath: filePath,
		board:    NewBoard(),
	}
}

// Load reads the kanban board from disk.
func (s *State) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Initialize new board
			s.board = NewBoard()
			return nil
		}
		return fmt.Errorf("failed to read kanban file: %w", err)
	}

	var board Board
	if err := json.Unmarshal(data, &board); err != nil {
		return fmt.Errorf("failed to parse kanban file: %w", err)
	}

	s.board = &board
	return nil
}

// Save writes the kanban board to disk.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.board.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(s.board, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize kanban: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create kanban directory: %w", err)
	}

	// Write atomically
	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write kanban file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		return fmt.Errorf("failed to rename kanban file: %w", err)
	}

	s.dirty = false
	return nil
}

// GetBoard returns a copy of the current board.
func (s *State) GetBoard() Board {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.board
}

// GetConfig returns the board configuration.
func (s *State) GetConfig() BoardConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.board.Config
}

// --- Ticket Queries ---

// GetTicket returns a ticket by ID.
func (s *State) GetTicket(id string) (*Ticket, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == id {
			return &s.board.Tickets[i], true
		}
	}
	return nil, false
}

// GetTicketsByStatus returns all tickets with the given status.
func (s *State) GetTicketsByStatus(status Status) []Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Ticket
	for _, t := range s.board.Tickets {
		if t.Status == status {
			result = append(result, t)
		}
	}

	// Sort by priority (lower number = higher priority)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Priority < result[j].Priority
	})

	return result
}

// GetTicketsByDomain returns all tickets for a specific domain.
func (s *State) GetTicketsByDomain(domain Domain) []Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Ticket
	for _, t := range s.board.Tickets {
		if t.Domain == domain {
			result = append(result, t)
		}
	}
	return result
}

// GetReadyTickets returns tickets ready to be worked on, sorted by priority.
func (s *State) GetReadyTickets() []Ticket {
	return s.GetTicketsByStatus(StatusReady)
}

// GetNextTicketForDomain returns the highest priority READY ticket for a domain
// that has no conflicts with in-progress work.
func (s *State) GetNextTicketForDomain(domain Domain) (*Ticket, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ready := s.GetTicketsByStatus(StatusReady)

	for _, t := range ready {
		if t.Domain != domain {
			continue
		}

		// Check dependencies are met
		if !s.dependenciesMet(t) {
			continue
		}

		// Check for conflicts
		if s.hasConflictUnsafe(&t) {
			continue
		}

		return &t, true
	}

	return nil, false
}

// dependenciesMet checks if all dependencies are done (internal, no lock).
func (s *State) dependenciesMet(t Ticket) bool {
	for _, depID := range t.Dependencies {
		for _, other := range s.board.Tickets {
			if other.ID == depID && other.Status != StatusDone {
				return false
			}
		}
	}
	return true
}

// GetInProgressCount returns the number of tickets currently being worked on.
func (s *State) GetInProgressCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	inProgressStatuses := []Status{StatusInDev, StatusInQA, StatusInUX, StatusInSec}

	for _, t := range s.board.Tickets {
		for _, status := range inProgressStatuses {
			if t.Status == status {
				count++
				break
			}
		}
	}
	return count
}

// GetStats returns summary statistics for the board.
func (s *State) GetStats() map[Status]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make(map[Status]int)
	for _, t := range s.board.Tickets {
		stats[t.Status]++
	}
	return stats
}

// --- Ticket Mutations ---

// AddTicket adds a new ticket to the board.
func (s *State) AddTicket(t Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for duplicate ID
	for _, existing := range s.board.Tickets {
		if existing.ID == t.ID {
			return fmt.Errorf("ticket with ID %s already exists", t.ID)
		}
	}

	// Set defaults
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	t.UpdatedAt = time.Now()

	// Initialize history
	t.History = append(t.History, HistoryEntry{
		Status: t.Status,
		At:     time.Now(),
		By:     "system",
		Note:   "Ticket created",
	})

	s.board.Tickets = append(s.board.Tickets, t)
	s.dirty = true
	return nil
}

// UpdateTicketStatus transitions a ticket to a new status.
func (s *State) UpdateTicketStatus(id string, newStatus Status, by string, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == id {
			oldStatus := s.board.Tickets[i].Status
			s.board.Tickets[i].Status = newStatus
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.board.Tickets[i].History = append(s.board.Tickets[i].History, HistoryEntry{
				Status: newStatus,
				At:     time.Now(),
				By:     by,
				Note:   fmt.Sprintf("%s -> %s: %s", oldStatus, newStatus, note),
			})
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", id)
}

// AssignAgent assigns an agent to a ticket.
func (s *State) AssignAgent(ticketID, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticketID {
			s.board.Tickets[i].AssignedAgent = agentID
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticketID)
}

// SetWorktree sets the worktree for a ticket.
func (s *State) SetWorktree(ticketID string, wt *Worktree) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticketID {
			s.board.Tickets[i].Worktree = wt
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticketID)
}

// AddSignoff records an agent's signoff.
func (s *State) AddSignoff(ticketID string, stage string, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticketID {
			now := time.Now().Format(time.RFC3339)
			switch stage {
			case "dev":
				s.board.Tickets[i].Signoffs.Dev = true
				s.board.Tickets[i].Signoffs.DevAgent = agentID
				s.board.Tickets[i].Signoffs.DevAt = now
			case "qa":
				s.board.Tickets[i].Signoffs.QA = true
				s.board.Tickets[i].Signoffs.QAAt = now
			case "ux":
				s.board.Tickets[i].Signoffs.UX = true
				s.board.Tickets[i].Signoffs.UXAt = now
			case "security":
				s.board.Tickets[i].Signoffs.Security = true
				s.board.Tickets[i].Signoffs.SecAt = now
			case "pm":
				s.board.Tickets[i].Signoffs.PM = true
				s.board.Tickets[i].Signoffs.PMAt = now
			default:
				return fmt.Errorf("unknown stage: %s", stage)
			}
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticketID)
}

// AddBug adds a bug to a ticket.
func (s *State) AddBug(ticketID string, bug Bug) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticketID {
			bug.FoundAt = time.Now()
			s.board.Tickets[i].Bugs = append(s.board.Tickets[i].Bugs, bug)
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticketID)
}

// UpdateNotes updates the agent notes for a ticket.
func (s *State) UpdateNotes(ticketID, notes string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticketID {
			s.board.Tickets[i].Notes = notes
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticketID)
}

// UpdateActivity updates the current activity for a ticket (shown in dashboard).
func (s *State) UpdateActivity(ticketID, activity, assignee string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticketID {
			s.board.Tickets[i].CurrentActivity = activity
			s.board.Tickets[i].Assignee = assignee
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticketID)
}

// ClearActivity clears the current activity when an agent finishes.
func (s *State) ClearActivity(ticketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticketID {
			s.board.Tickets[i].CurrentActivity = ""
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticketID)
}

// UpdateTicket updates a ticket in the board.
func (s *State) UpdateTicket(ticket *Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.Tickets {
		if s.board.Tickets[i].ID == ticket.ID {
			s.board.Tickets[i] = *ticket
			s.board.Tickets[i].UpdatedAt = time.Now()
			s.dirty = true
			return nil
		}
	}
	return fmt.Errorf("ticket %s not found", ticket.ID)
}

// --- Iteration Management ---

// SetIteration sets the current iteration.
func (s *State) SetIteration(iter *Iteration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.board.Iteration = iter
	s.dirty = true
}

// GetIteration returns the current iteration.
func (s *State) GetIteration() *Iteration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.board.Iteration
}

// IsIterationComplete returns true if all tickets in the iteration are done.
func (s *State) IsIterationComplete() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// No tickets means nothing to do
	if len(s.board.Tickets) == 0 {
		return true
	}

	// Check if all tickets are done (iteration or not)
	for _, t := range s.board.Tickets {
		if t.Status != StatusDone && t.Status != StatusBacklog {
			return false
		}
	}
	return true
}

// --- Active Runs ---

// AddActiveRun records a new agent run.
func (s *State) AddActiveRun(run AgentRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.board.ActiveRuns = append(s.board.ActiveRuns, run)
	s.dirty = true
}

// CompleteRun marks a run as complete.
func (s *State) CompleteRun(runID string, status string, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.board.ActiveRuns {
		if s.board.ActiveRuns[i].ID == runID {
			s.board.ActiveRuns[i].Status = status
			s.board.ActiveRuns[i].EndedAt = time.Now()
			s.board.ActiveRuns[i].Output = output
			s.dirty = true
			return
		}
	}
}

// GetActiveRuns returns all currently running agents.
func (s *State) GetActiveRuns() []AgentRun {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var active []AgentRun
	for _, run := range s.board.ActiveRuns {
		if run.Status == "running" {
			active = append(active, run)
		}
	}
	return active
}

// GetActiveDevRuns returns only dev agent runs (for applying dev-specific limits).
// Dev agents have names starting with "dev-" (e.g., dev-frontend, dev-backend, dev-infra).
func (s *State) GetActiveDevRuns() []AgentRun {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var active []AgentRun
	for _, run := range s.board.ActiveRuns {
		if run.Status == "running" && isDevAgent(run.Agent) {
			active = append(active, run)
		}
	}
	return active
}

// isDevAgent checks if an agent name represents a dev agent.
func isDevAgent(agentName string) bool {
	return len(agentName) >= 4 && agentName[:4] == "dev-"
}

// CleanupStaleRuns removes runs older than the given duration.
func (s *State) CleanupStaleRuns(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	var active []AgentRun
	for _, run := range s.board.ActiveRuns {
		if run.EndedAt.IsZero() || run.EndedAt.After(cutoff) {
			active = append(active, run)
		}
	}
	s.board.ActiveRuns = active
	s.dirty = true
}

// --- Additional StateStore interface methods ---

// GetAllTickets returns all tickets.
func (s *State) GetAllTickets() ([]Ticket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.board.Tickets, nil
}

// GetTicketsByParent returns all sub-tickets for a given parent.
func (s *State) GetTicketsByParent(parentID string) []Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Ticket
	for _, t := range s.board.Tickets {
		if t.ParentID == parentID {
			result = append(result, t)
		}
	}

	// Sort by parallel group, then priority
	sort.Slice(result, func(i, j int) bool {
		if result[i].ParallelGroup != result[j].ParallelGroup {
			return result[i].ParallelGroup < result[j].ParallelGroup
		}
		return result[i].Priority < result[j].Priority
	})

	return result
}

// CreateTicket creates a new ticket (alias for AddTicket for interface compatibility).
func (s *State) CreateTicket(t *Ticket) error {
	return s.AddTicket(*t)
}

// AddRun adds an agent run (alias for AddActiveRun for interface compatibility).
func (s *State) AddRun(run *AgentRun) error {
	s.AddActiveRun(*run)
	return nil
}

// GetActiveRunsForTicket returns active runs for a specific ticket.
func (s *State) GetActiveRunsForTicket(ticketID string) []AgentRun {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []AgentRun
	for _, run := range s.board.ActiveRuns {
		if run.Status == "running" && run.TicketID == ticketID {
			result = append(result, run)
		}
	}
	return result
}
