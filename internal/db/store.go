package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/madhatter5501/Factory/kanban"
)

// Store implements kanban state storage using SQLite.
type Store struct {
	db *DB
}

// NewStore creates a new SQLite-backed store.
func NewStore(db *DB) *Store {
	return &Store{db: db}
}

// --- Ticket Operations ---

// CreateTicket creates a new ticket.
func (s *Store) CreateTicket(t *kanban.Ticket) error {
	files, _ := json.Marshal(t.Files)
	deps, _ := json.Marshal(t.Dependencies)
	criteria, _ := json.Marshal(t.AcceptanceCriteria)
	requirements, _ := json.Marshal(t.Requirements)
	signoffs, _ := json.Marshal(t.Signoffs)
	bugs, _ := json.Marshal(t.Bugs)
	conversation, _ := json.Marshal(t.Conversation)

	_, err := s.db.Exec(`
		INSERT INTO tickets (
			id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		t.ID, t.Title, t.Description, t.Domain, t.Priority, t.Type, t.Status,
		t.AssignedAgent, t.Assignee, files, deps, criteria,
		requirements, signoffs, bugs, t.Notes,
		worktreePath(t.Worktree), worktreeBranch(t.Worktree), worktreeActive(t.Worktree),
		conversation, t.ParentID, t.ParallelGroup,
		t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create ticket: %w", err)
	}

	// Add initial history entry
	return s.addHistory(t.ID, string(t.Status), "system", "Ticket created")
}

// GetTicket retrieves a ticket by ID.
func (s *Store) GetTicket(id string) (*kanban.Ticket, bool) {
	row := s.db.QueryRow(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets WHERE id = ?
	`, id)

	t, err := scanTicket(row)
	if err != nil {
		return nil, false
	}

	// Load history
	history, err := s.getHistory(id)
	if err == nil {
		t.History = history
	}

	return t, true
}

// GetAllTickets retrieves all tickets.
func (s *Store) GetAllTickets() ([]kanban.Ticket, error) {
	rows, err := s.db.Query(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets ORDER BY priority, created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query tickets: %w", err)
	}
	defer rows.Close()

	var tickets []kanban.Ticket
	for rows.Next() {
		t, err := scanTicketRows(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, *t)
	}

	return tickets, nil
}

// GetTicketsByStatus retrieves tickets with a specific status.
func (s *Store) GetTicketsByStatus(status kanban.Status) []kanban.Ticket {
	rows, err := s.db.Query(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets WHERE status = ? ORDER BY priority, created_at
	`, status)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tickets []kanban.Ticket
	for rows.Next() {
		t, err := scanTicketRows(rows)
		if err != nil {
			continue
		}
		tickets = append(tickets, *t)
	}

	return tickets
}

// UpdateTicket updates an existing ticket.
func (s *Store) UpdateTicket(t *kanban.Ticket) error {
	files, _ := json.Marshal(t.Files)
	deps, _ := json.Marshal(t.Dependencies)
	criteria, _ := json.Marshal(t.AcceptanceCriteria)
	requirements, _ := json.Marshal(t.Requirements)
	signoffs, _ := json.Marshal(t.Signoffs)
	bugs, _ := json.Marshal(t.Bugs)
	conversation, _ := json.Marshal(t.Conversation)

	_, err := s.db.Exec(`
		UPDATE tickets SET
			title = ?, description = ?, domain = ?, priority = ?, type = ?, status = ?,
			assigned_agent = ?, assignee = ?, files = ?, dependencies = ?, acceptance_criteria = ?,
			requirements = ?, signoffs = ?, bugs = ?, notes = ?,
			worktree_path = ?, worktree_branch = ?, worktree_active = ?,
			conversation = ?, parent_id = ?, parallel_group = ?,
			updated_at = ?
		WHERE id = ?
	`,
		t.Title, t.Description, t.Domain, t.Priority, t.Type, t.Status,
		t.AssignedAgent, t.Assignee, files, deps, criteria,
		requirements, signoffs, bugs, t.Notes,
		worktreePath(t.Worktree), worktreeBranch(t.Worktree), worktreeActive(t.Worktree),
		conversation, t.ParentID, t.ParallelGroup,
		time.Now(), t.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update ticket: %w", err)
	}

	return nil
}

// UpdateTicketStatus updates a ticket's status and records history.
func (s *Store) UpdateTicketStatus(id string, status kanban.Status, by, note string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		UPDATE tickets SET status = ?, updated_at = ? WHERE id = ?
	`, status, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO ticket_history (ticket_id, status, changed_by, note)
		VALUES (?, ?, ?, ?)
	`, id, status, by, note)
	if err != nil {
		return fmt.Errorf("failed to add history: %w", err)
	}

	return tx.Commit()
}

// DeleteTicket deletes a ticket.
func (s *Store) DeleteTicket(id string) error {
	_, err := s.db.Exec("DELETE FROM tickets WHERE id = ?", id)
	return err
}

// AddHistoryEntry adds a history entry without changing status.
func (s *Store) AddHistoryEntry(id string, status kanban.Status, by, note string) error {
	_, err := s.db.Exec(`
		INSERT INTO ticket_history (ticket_id, status, changed_by, note)
		VALUES (?, ?, ?, ?)
	`, id, status, by, note)
	return err
}

// --- Agent Runs ---

// AddRun adds an agent run.
func (s *Store) AddRun(run *kanban.AgentRun) error {
	_, err := s.db.Exec(`
		INSERT INTO agent_runs (id, agent, ticket_id, worktree, started_at, status)
		VALUES (?, ?, ?, ?, ?, ?)
	`, run.ID, run.Agent, run.TicketID, run.Worktree, run.StartedAt, run.Status)
	return err
}

// CompleteRun marks a run as complete.
func (s *Store) CompleteRun(id, status, output string) {
	s.db.Exec(`
		UPDATE agent_runs SET ended_at = ?, status = ?, output = ? WHERE id = ?
	`, time.Now(), status, output, id)
}

// GetActiveRuns returns all running agent runs.
func (s *Store) GetActiveRuns() []kanban.AgentRun {
	rows, err := s.db.Query(`
		SELECT id, agent, ticket_id, worktree, started_at, ended_at, status, output
		FROM agent_runs WHERE status = 'running'
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var runs []kanban.AgentRun
	for rows.Next() {
		var run kanban.AgentRun
		var endedAt sql.NullTime
		var output sql.NullString
		err := rows.Scan(&run.ID, &run.Agent, &run.TicketID, &run.Worktree,
			&run.StartedAt, &endedAt, &run.Status, &output)
		if err != nil {
			continue
		}
		if endedAt.Valid {
			run.EndedAt = endedAt.Time
		}
		if output.Valid {
			run.Output = output.String
		}
		runs = append(runs, run)
	}

	return runs
}

// --- Config ---

// GetConfigValue retrieves a config value by key.
func (s *Store) GetConfigValue(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetConfig sets a config value.
func (s *Store) SetConfig(key, value string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)
	`, key, value)
	return err
}

// --- Stats ---

// GetStats returns ticket counts by status.
func (s *Store) GetStats() map[kanban.Status]int {
	rows, err := s.db.Query(`
		SELECT status, COUNT(*) FROM tickets GROUP BY status
	`)
	if err != nil {
		return make(map[kanban.Status]int)
	}
	defer rows.Close()

	stats := make(map[kanban.Status]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			continue
		}
		stats[kanban.Status(status)] = count
	}

	return stats
}

// --- Helpers ---

func (s *Store) addHistory(ticketID, status, by, note string) error {
	_, err := s.db.Exec(`
		INSERT INTO ticket_history (ticket_id, status, changed_by, note)
		VALUES (?, ?, ?, ?)
	`, ticketID, status, by, note)
	return err
}

func (s *Store) getHistory(ticketID string) ([]kanban.HistoryEntry, error) {
	rows, err := s.db.Query(`
		SELECT status, changed_by, note, created_at
		FROM ticket_history WHERE ticket_id = ? ORDER BY created_at
	`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []kanban.HistoryEntry
	for rows.Next() {
		var h kanban.HistoryEntry
		if err := rows.Scan(&h.Status, &h.By, &h.Note, &h.At); err != nil {
			return nil, err
		}
		history = append(history, h)
	}

	return history, nil
}

// Scanner interface for both sql.Row and sql.Rows
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanTicket(row *sql.Row) (*kanban.Ticket, error) {
	return scanTicketGeneric(row)
}

func scanTicketRows(rows *sql.Rows) (*kanban.Ticket, error) {
	return scanTicketGeneric(rows)
}

func scanTicketGeneric(s scanner) (*kanban.Ticket, error) {
	var t kanban.Ticket
	var files, deps, criteria, requirements, signoffs, bugs, conversation sql.NullString
	var wtPath, wtBranch sql.NullString
	var wtActive int
	var parentID sql.NullString
	var assignedAgent, assignee, notes, description sql.NullString

	err := s.Scan(
		&t.ID, &t.Title, &description, &t.Domain, &t.Priority, &t.Type, &t.Status,
		&assignedAgent, &assignee, &files, &deps, &criteria,
		&requirements, &signoffs, &bugs, &notes,
		&wtPath, &wtBranch, &wtActive,
		&conversation, &parentID, &t.ParallelGroup,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Handle nullable string fields
	if description.Valid {
		t.Description = description.String
	}
	if assignedAgent.Valid {
		t.AssignedAgent = assignedAgent.String
	}
	if assignee.Valid {
		t.Assignee = assignee.String
	}
	if notes.Valid {
		t.Notes = notes.String
	}

	// Unmarshal JSON fields
	if files.Valid {
		json.Unmarshal([]byte(files.String), &t.Files)
	}
	if deps.Valid {
		json.Unmarshal([]byte(deps.String), &t.Dependencies)
	}
	if criteria.Valid {
		json.Unmarshal([]byte(criteria.String), &t.AcceptanceCriteria)
	}
	if requirements.Valid {
		json.Unmarshal([]byte(requirements.String), &t.Requirements)
	}
	if signoffs.Valid {
		json.Unmarshal([]byte(signoffs.String), &t.Signoffs)
	}
	if bugs.Valid {
		json.Unmarshal([]byte(bugs.String), &t.Bugs)
	}
	if conversation.Valid {
		json.Unmarshal([]byte(conversation.String), &t.Conversation)
	}

	// Parent ID
	if parentID.Valid {
		t.ParentID = parentID.String
	}

	// Worktree
	if wtPath.Valid && wtPath.String != "" {
		t.Worktree = &kanban.Worktree{
			Path:   wtPath.String,
			Branch: wtBranch.String,
			Active: wtActive == 1,
		}
	}

	return &t, nil
}

func worktreePath(w *kanban.Worktree) string {
	if w == nil {
		return ""
	}
	return w.Path
}

func worktreeBranch(w *kanban.Worktree) string {
	if w == nil {
		return ""
	}
	return w.Branch
}

func worktreeActive(w *kanban.Worktree) int {
	if w == nil || !w.Active {
		return 0
	}
	return 1
}

// --- StateStore Interface Implementation ---

// Load is a no-op for SQLite (data is always in DB).
func (s *Store) Load() error {
	return nil
}

// Save is a no-op for SQLite (changes are immediate).
func (s *Store) Save() error {
	return nil
}

// GetBoard returns a Board constructed from the database.
func (s *Store) GetBoard() kanban.Board {
	tickets, _ := s.GetAllTickets()
	config := s.getBoardConfig()
	runs := s.GetActiveRuns()

	return kanban.Board{
		Version:    "1.0.0",
		UpdatedAt:  time.Now(),
		Tickets:    tickets,
		Config:     config,
		ActiveRuns: runs,
	}
}

// GetConfig returns the board configuration.
func (s *Store) GetConfig() kanban.BoardConfig {
	return s.getBoardConfig()
}

func (s *Store) getBoardConfig() kanban.BoardConfig {
	config := kanban.BoardConfig{
		WorktreeDir:      ".worktrees",
		MaxParallelAgents: 3,
		MaxTicketsPerAgent: 1,
		MainBranch:       "main",
		BranchPrefix:     "feat/",
		SquashOnMerge:    true,
		RebaseOnUpdate:   true,
	}

	if v, _ := s.GetConfigValue("worktree_dir"); v != "" {
		config.WorktreeDir = v
	}
	if v, _ := s.GetConfigValue("main_branch"); v != "" {
		config.MainBranch = v
	}
	if v, _ := s.GetConfigValue("branch_prefix"); v != "" {
		config.BranchPrefix = v
	}

	return config
}

// GetTicketsByDomain returns all tickets for a specific domain.
func (s *Store) GetTicketsByDomain(domain kanban.Domain) []kanban.Ticket {
	rows, err := s.db.Query(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets WHERE domain = ? ORDER BY priority, created_at
	`, domain)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tickets []kanban.Ticket
	for rows.Next() {
		t, err := scanTicketRows(rows)
		if err != nil {
			continue
		}
		tickets = append(tickets, *t)
	}
	return tickets
}

// GetTicketsByParent returns all sub-tickets for a given parent ticket.
func (s *Store) GetTicketsByParent(parentID string) []kanban.Ticket {
	rows, err := s.db.Query(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets WHERE parent_id = ? ORDER BY parallel_group, priority, created_at
	`, parentID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tickets []kanban.Ticket
	for rows.Next() {
		t, err := scanTicketRows(rows)
		if err != nil {
			continue
		}
		tickets = append(tickets, *t)
	}
	return tickets
}

// GetTicketsByRefiningRound returns tickets in any REFINING_ROUND status.
func (s *Store) GetTicketsByRefiningRound() []kanban.Ticket {
	rows, err := s.db.Query(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets WHERE status LIKE 'REFINING_ROUND%' ORDER BY priority, created_at
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tickets []kanban.Ticket
	for rows.Next() {
		t, err := scanTicketRows(rows)
		if err != nil {
			continue
		}
		tickets = append(tickets, *t)
	}
	return tickets
}

// GetReadyTickets returns tickets ready to be worked on.
func (s *Store) GetReadyTickets() []kanban.Ticket {
	tickets := s.GetTicketsByStatus(kanban.StatusReady)
	return tickets
}

// GetNextTicketForDomain returns the highest priority READY ticket for a domain.
func (s *Store) GetNextTicketForDomain(domain kanban.Domain) (*kanban.Ticket, bool) {
	ready := s.GetTicketsByStatus(kanban.StatusReady)

	for _, t := range ready {
		if t.Domain != domain {
			continue
		}
		// Check dependencies are met
		if !s.dependenciesMet(&t) {
			continue
		}
		// Check for conflicts
		if s.hasConflict(&t) {
			continue
		}
		return &t, true
	}
	return nil, false
}

func (s *Store) dependenciesMet(t *kanban.Ticket) bool {
	for _, depID := range t.Dependencies {
		dep, found := s.GetTicket(depID)
		if !found {
			continue
		}
		if dep.Status != kanban.StatusDone {
			return false
		}
	}
	return true
}

func (s *Store) hasConflict(t *kanban.Ticket) bool {
	// Check if any in-progress ticket touches the same files
	inProgress := s.GetTicketsByStatus(kanban.StatusInDev)
	for _, other := range inProgress {
		if s.filesOverlap(t.Files, other.Files) {
			return true
		}
	}
	return false
}

func (s *Store) filesOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	fileSet := make(map[string]bool)
	for _, f := range a {
		fileSet[f] = true
	}
	for _, f := range b {
		if fileSet[f] {
			return true
		}
	}
	return false
}

// GetInProgressCount returns the number of tickets currently being worked on.
func (s *Store) GetInProgressCount() int {
	var count int
	s.db.QueryRow(`
		SELECT COUNT(*) FROM tickets
		WHERE status IN ('IN_DEV', 'IN_QA', 'IN_UX', 'IN_SEC')
	`).Scan(&count)
	return count
}

// AddTicket adds a new ticket (wrapper around CreateTicket).
func (s *Store) AddTicket(t kanban.Ticket) error {
	return s.CreateTicket(&t)
}

// AssignAgent assigns an agent to a ticket.
func (s *Store) AssignAgent(ticketID, agentID string) error {
	_, err := s.db.Exec(`
		UPDATE tickets SET assigned_agent = ?, updated_at = ? WHERE id = ?
	`, agentID, time.Now(), ticketID)
	return err
}

// SetWorktree sets the worktree for a ticket.
func (s *Store) SetWorktree(ticketID string, wt *kanban.Worktree) error {
	_, err := s.db.Exec(`
		UPDATE tickets SET
			worktree_path = ?, worktree_branch = ?, worktree_active = ?, updated_at = ?
		WHERE id = ?
	`, worktreePath(wt), worktreeBranch(wt), worktreeActive(wt), time.Now(), ticketID)
	return err
}

// AddSignoff records an agent's signoff.
func (s *Store) AddSignoff(ticketID string, stage string, agentID string) error {
	t, found := s.GetTicket(ticketID)
	if !found {
		return fmt.Errorf("ticket not found: %s", ticketID)
	}

	now := time.Now().Format(time.RFC3339)
	switch stage {
	case "dev":
		t.Signoffs.Dev = true
		t.Signoffs.DevAgent = agentID
		t.Signoffs.DevAt = now
	case "qa":
		t.Signoffs.QA = true
		t.Signoffs.QAAt = now
	case "ux":
		t.Signoffs.UX = true
		t.Signoffs.UXAt = now
	case "security":
		t.Signoffs.Security = true
		t.Signoffs.SecAt = now
	case "pm":
		t.Signoffs.PM = true
		t.Signoffs.PMAt = now
	default:
		return fmt.Errorf("unknown stage: %s", stage)
	}

	return s.UpdateTicket(t)
}

// AddBug adds a bug to a ticket.
func (s *Store) AddBug(ticketID string, bug kanban.Bug) error {
	t, found := s.GetTicket(ticketID)
	if !found {
		return fmt.Errorf("ticket not found: %s", ticketID)
	}

	bug.FoundAt = time.Now()
	t.Bugs = append(t.Bugs, bug)
	return s.UpdateTicket(t)
}

// UpdateNotes updates the agent notes for a ticket.
func (s *Store) UpdateNotes(ticketID, notes string) error {
	_, err := s.db.Exec(`
		UPDATE tickets SET notes = ?, updated_at = ? WHERE id = ?
	`, notes, time.Now(), ticketID)
	return err
}

// UpdateActivity updates the current activity for a ticket.
func (s *Store) UpdateActivity(ticketID, activity, assignee string) error {
	t, found := s.GetTicket(ticketID)
	if !found {
		return fmt.Errorf("ticket not found: %s", ticketID)
	}
	t.CurrentActivity = activity
	t.Assignee = assignee
	return s.UpdateTicket(t)
}

// ClearActivity clears the current activity when an agent finishes.
func (s *Store) ClearActivity(ticketID string) error {
	t, found := s.GetTicket(ticketID)
	if !found {
		return fmt.Errorf("ticket not found: %s", ticketID)
	}
	t.CurrentActivity = ""
	return s.UpdateTicket(t)
}

// --- Iteration ---

// SetIteration sets the current iteration.
func (s *Store) SetIteration(iter *kanban.Iteration) {
	if iter == nil {
		s.SetConfig("iteration", "")
		return
	}
	data, _ := json.Marshal(iter)
	s.SetConfig("iteration", string(data))
}

// GetIteration returns the current iteration.
func (s *Store) GetIteration() *kanban.Iteration {
	v, _ := s.GetConfigValue("iteration")
	if v == "" {
		return nil
	}
	var iter kanban.Iteration
	if err := json.Unmarshal([]byte(v), &iter); err != nil {
		return nil
	}
	return &iter
}

// IsIterationComplete returns true if all tickets are done.
func (s *Store) IsIterationComplete() bool {
	var count int
	s.db.QueryRow(`
		SELECT COUNT(*) FROM tickets
		WHERE status NOT IN ('DONE', 'BACKLOG')
	`).Scan(&count)
	return count == 0
}

// --- Active Runs ---

// AddActiveRun records a new agent run.
func (s *Store) AddActiveRun(run kanban.AgentRun) {
	s.AddRun(&run)
}

// GetActiveDevRuns returns only dev agent runs.
func (s *Store) GetActiveDevRuns() []kanban.AgentRun {
	rows, err := s.db.Query(`
		SELECT id, agent, ticket_id, worktree, started_at, ended_at, status, output
		FROM agent_runs WHERE status = 'running' AND agent LIKE 'dev-%'
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var runs []kanban.AgentRun
	for rows.Next() {
		var run kanban.AgentRun
		var endedAt sql.NullTime
		var output sql.NullString
		err := rows.Scan(&run.ID, &run.Agent, &run.TicketID, &run.Worktree,
			&run.StartedAt, &endedAt, &run.Status, &output)
		if err != nil {
			continue
		}
		if endedAt.Valid {
			run.EndedAt = endedAt.Time
		}
		if output.Valid {
			run.Output = output.String
		}
		runs = append(runs, run)
	}
	return runs
}

// CleanupStaleRuns removes runs older than the given duration.
func (s *Store) CleanupStaleRuns(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	s.db.Exec(`
		DELETE FROM agent_runs
		WHERE ended_at IS NOT NULL AND ended_at < ?
	`, cutoff)
}

// --- PRD Conversation Methods ---

// UpdateConversation updates just the conversation field of a ticket.
func (s *Store) UpdateConversation(ticketID string, conv *kanban.PRDConversation) error {
	conversation, _ := json.Marshal(conv)
	_, err := s.db.Exec(`
		UPDATE tickets SET conversation = ?, updated_at = ? WHERE id = ?
	`, conversation, time.Now(), ticketID)
	return err
}

// AreAllSubTicketsDone checks if all sub-tickets of a parent are complete.
func (s *Store) AreAllSubTicketsDone(parentID string) bool {
	var total, done int
	s.db.QueryRow(`
		SELECT COUNT(*) FROM tickets WHERE parent_id = ?
	`, parentID).Scan(&total)
	s.db.QueryRow(`
		SELECT COUNT(*) FROM tickets WHERE parent_id = ? AND status = 'DONE'
	`, parentID).Scan(&done)
	return total > 0 && total == done
}

// GetTicketsByParallelGroup returns tickets in the same parallel group.
func (s *Store) GetTicketsByParallelGroup(group int) []kanban.Ticket {
	rows, err := s.db.Query(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets WHERE parallel_group = ? ORDER BY priority, created_at
	`, group)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tickets []kanban.Ticket
	for rows.Next() {
		t, err := scanTicketRows(rows)
		if err != nil {
			continue
		}
		tickets = append(tickets, *t)
	}
	return tickets
}

// GetActiveRunsForTicket returns all active runs for a specific ticket.
func (s *Store) GetActiveRunsForTicket(ticketID string) []kanban.AgentRun {
	rows, err := s.db.Query(`
		SELECT id, agent, ticket_id, worktree, started_at, ended_at, status, output
		FROM agent_runs WHERE status = 'running' AND ticket_id = ?
	`, ticketID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var runs []kanban.AgentRun
	for rows.Next() {
		var run kanban.AgentRun
		var endedAt sql.NullTime
		var output sql.NullString
		err := rows.Scan(&run.ID, &run.Agent, &run.TicketID, &run.Worktree,
			&run.StartedAt, &endedAt, &run.Status, &output)
		if err != nil {
			continue
		}
		if endedAt.Valid {
			run.EndedAt = endedAt.Time
		}
		if output.Valid {
			run.Output = output.String
		}
		runs = append(runs, run)
	}
	return runs
}
