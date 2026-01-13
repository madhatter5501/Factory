package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"factory/agents/provider"
	"factory/kanban"
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

	// Load tags for all tickets (batch to avoid N+1 queries)
	if err := s.loadTagsForTickets(tickets); err != nil {
		// Log but don't fail - tags are supplementary
		_ = err
	}

	return tickets, nil
}

// loadTagsForTickets efficiently loads tags for multiple tickets in a single query.
func (s *Store) loadTagsForTickets(tickets []kanban.Ticket) error {
	if len(tickets) == 0 {
		return nil
	}

	// Build ticket ID list for IN clause
	ticketIDs := make([]interface{}, len(tickets))
	placeholders := make([]byte, 0, len(tickets)*2)
	for i, t := range tickets {
		ticketIDs[i] = t.ID
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}

	query := `
		SELECT tt.ticket_id, t.id, t.name, t.type, t.color, t.description
		FROM tags t
		INNER JOIN ticket_tags tt ON t.id = tt.tag_id
		WHERE tt.ticket_id IN (` + string(placeholders) + `)
		ORDER BY t.type, t.name
	`

	rows, err := s.db.Query(query, ticketIDs...)
	if err != nil {
		return fmt.Errorf("failed to query tags for tickets: %w", err)
	}
	defer rows.Close()

	// Build map of ticket ID -> tags
	tagsByTicket := make(map[string][]kanban.Tag)
	for rows.Next() {
		var ticketID string
		var tag kanban.Tag
		var description sql.NullString
		if err := rows.Scan(&ticketID, &tag.ID, &tag.Name, &tag.Type, &tag.Color, &description); err != nil {
			continue
		}
		if description.Valid {
			tag.Description = description.String
		}
		tagsByTicket[ticketID] = append(tagsByTicket[ticketID], tag)
	}

	// Assign tags to tickets
	for i := range tickets {
		if tags, ok := tagsByTicket[tickets[i].ID]; ok {
			tickets[i].Tags = tags
		}
	}

	return nil
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

// --- Provider Config ---

// GetAgentProviderConfig retrieves the provider config for an agent type.
func (s *Store) GetAgentProviderConfig(agentType string) (*provider.AgentProviderConfig, error) {
	var cfg provider.AgentProviderConfig
	var systemPrompt sql.NullString
	err := s.db.QueryRow(`
		SELECT agent_type, provider, model, system_prompt, updated_at
		FROM agent_provider_config WHERE agent_type = ?
	`, agentType).Scan(&cfg.AgentType, &cfg.Provider, &cfg.Model, &systemPrompt, &cfg.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if systemPrompt.Valid {
		cfg.SystemPrompt = systemPrompt.String
	}
	return &cfg, nil
}

// SetAgentProviderConfig sets the provider config for an agent type.
func (s *Store) SetAgentProviderConfig(agentType, providerName, model string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO agent_provider_config (agent_type, provider, model, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, agentType, providerName, model)
	return err
}

// SetAgentSystemPrompt updates the system prompt for an agent type.
func (s *Store) SetAgentSystemPrompt(agentType, systemPrompt string) error {
	_, err := s.db.Exec(`
		UPDATE agent_provider_config SET system_prompt = ?, updated_at = CURRENT_TIMESTAMP
		WHERE agent_type = ?
	`, systemPrompt, agentType)
	return err
}

// GetAllAgentProviderConfigs retrieves all agent provider configs.
func (s *Store) GetAllAgentProviderConfigs() ([]provider.AgentProviderConfig, error) {
	rows, err := s.db.Query(`
		SELECT agent_type, provider, model, system_prompt, updated_at
		FROM agent_provider_config ORDER BY agent_type
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []provider.AgentProviderConfig
	for rows.Next() {
		var cfg provider.AgentProviderConfig
		var systemPrompt sql.NullString
		if err := rows.Scan(&cfg.AgentType, &cfg.Provider, &cfg.Model, &systemPrompt, &cfg.UpdatedAt); err != nil {
			return nil, err
		}
		if systemPrompt.Valid {
			cfg.SystemPrompt = systemPrompt.String
		}
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
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
	for _, dep := range t.Dependencies {
		// Dependencies can be stored as IDs or titles, try both
		depTicket, found := s.GetTicket(dep)
		if !found {
			// Try looking up by title
			depTicket, found = s.GetTicketByTitle(dep)
		}
		if !found {
			// If dependency not found, it's not met (conservative approach)
			// This prevents tickets from starting when dependencies are missing
			return false
		}
		if depTicket.Status != kanban.StatusDone {
			return false
		}
	}
	return true
}

// GetTicketByTitle retrieves a ticket by its title.
func (s *Store) GetTicketByTitle(title string) (*kanban.Ticket, bool) {
	row := s.db.QueryRow(`
		SELECT id, title, description, domain, priority, type, status,
			assigned_agent, assignee, files, dependencies, acceptance_criteria,
			requirements, signoffs, bugs, notes,
			worktree_path, worktree_branch, worktree_active,
			conversation, parent_id, parallel_group,
			created_at, updated_at
		FROM tickets WHERE title = ?
	`, title)

	t, err := scanTicket(row)
	if err != nil {
		return nil, false
	}

	return t, true
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

// CleanupStaleRuns removes completed runs older than the given duration.
func (s *Store) CleanupStaleRuns(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	s.db.Exec(`
		DELETE FROM agent_runs
		WHERE ended_at IS NOT NULL AND ended_at < ?
	`, cutoff)
}

// CleanupStaleRunningAgents marks runs that have been "running" for too long as failed.
// This handles cases where agents crash without proper cleanup.
func (s *Store) CleanupStaleRunningAgents(maxRunDuration time.Duration) int {
	cutoff := time.Now().Add(-maxRunDuration)
	now := time.Now()

	// Get all running agents and compare times in Go (SQLite string comparison is unreliable)
	rows, err := s.db.Query(`
		SELECT id, started_at FROM agent_runs WHERE status = 'running'
	`)
	if err != nil {
		return 0
	}
	defer rows.Close()

	var staleIDs []string
	for rows.Next() {
		var id string
		var startedAt time.Time
		if err := rows.Scan(&id, &startedAt); err != nil {
			continue
		}
		if startedAt.Before(cutoff) {
			staleIDs = append(staleIDs, id)
		}
	}

	if len(staleIDs) == 0 {
		return 0
	}

	// Mark stale runs as failed
	for _, id := range staleIDs {
		s.db.Exec(`
			UPDATE agent_runs
			SET status = 'failed', ended_at = ?, output = 'Marked as stale - exceeded max run duration'
			WHERE id = ?
		`, now, id)
	}

	return len(staleIDs)
}

// CleanupOrphanedRunningAgents marks ALL running agents as failed on startup.
// This is called during factory initialization to clean up runs from a previous
// factory session that was killed without proper cleanup.
func (s *Store) CleanupOrphanedRunningAgents() int {
	now := time.Now()

	result, err := s.db.Exec(`
		UPDATE agent_runs
		SET status = 'failed', ended_at = ?, output = 'Orphaned run from previous factory session'
		WHERE status = 'running'
	`, now)
	if err != nil {
		return 0
	}

	affected, _ := result.RowsAffected()
	return int(affected)
}

// IsAgentRunning checks if an agent of the given type is already running for a ticket.
func (s *Store) IsAgentRunning(ticketID, agentType string) bool {
	var count int
	s.db.QueryRow(`
		SELECT COUNT(*) FROM agent_runs
		WHERE ticket_id = ? AND agent = ? AND status = 'running'
	`, ticketID, agentType).Scan(&count)
	return count > 0
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

// --- Audit Logging ---

// AddAuditEntry records an audit log entry for agent activity.
func (s *Store) AddAuditEntry(entry *kanban.AuditEntry) error {
	_, err := s.db.Exec(`
		INSERT INTO agent_audit_log (
			id, run_id, ticket_id, agent, event_type, event_data,
			token_input, token_output, duration_ms, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		entry.ID, entry.RunID, entry.TicketID, entry.Agent, entry.EventType, entry.EventData,
		entry.TokenInput, entry.TokenOutput, entry.DurationMs, entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to add audit entry: %w", err)
	}
	return nil
}

// GetAuditEntriesByRun returns all audit entries for a specific agent run.
func (s *Store) GetAuditEntriesByRun(runID string) ([]kanban.AuditEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, run_id, ticket_id, agent, event_type, event_data,
			token_input, token_output, duration_ms, created_at
		FROM agent_audit_log WHERE run_id = ? ORDER BY created_at
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit entries: %w", err)
	}
	defer rows.Close()

	return scanAuditEntries(rows)
}

// GetAuditEntriesByTicket returns all audit entries for a specific ticket.
func (s *Store) GetAuditEntriesByTicket(ticketID string) ([]kanban.AuditEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, run_id, ticket_id, agent, event_type, event_data,
			token_input, token_output, duration_ms, created_at
		FROM agent_audit_log WHERE ticket_id = ? ORDER BY created_at
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit entries: %w", err)
	}
	defer rows.Close()

	return scanAuditEntries(rows)
}

// GetRecentAuditEntries returns the most recent audit entries across all tickets.
func (s *Store) GetRecentAuditEntries(limit int) ([]kanban.AuditEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, run_id, ticket_id, agent, event_type, event_data,
			token_input, token_output, duration_ms, created_at
		FROM agent_audit_log ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit entries: %w", err)
	}
	defer rows.Close()

	return scanAuditEntries(rows)
}

// GetAuditEntryCount returns the total number of audit entries.
func (s *Store) GetAuditEntryCount() int {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM agent_audit_log").Scan(&count)
	return count
}

func scanAuditEntries(rows *sql.Rows) ([]kanban.AuditEntry, error) {
	var entries []kanban.AuditEntry
	for rows.Next() {
		var e kanban.AuditEntry
		var runID, eventData sql.NullString
		var tokenIn, tokenOut, durationMs sql.NullInt64

		err := rows.Scan(
			&e.ID, &runID, &e.TicketID, &e.Agent, &e.EventType, &eventData,
			&tokenIn, &tokenOut, &durationMs, &e.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		if runID.Valid {
			e.RunID = runID.String
		}
		if eventData.Valid {
			e.EventData = eventData.String
		}
		if tokenIn.Valid {
			e.TokenInput = int(tokenIn.Int64)
		}
		if tokenOut.Valid {
			e.TokenOutput = int(tokenOut.Int64)
		}
		if durationMs.Valid {
			e.DurationMs = int(durationMs.Int64)
		}

		entries = append(entries, e)
	}
	return entries, nil
}

// --- Ticket Conversations ---

// CreateConversation creates a new conversation thread for a ticket.
func (s *Store) CreateConversation(conv *kanban.TicketConversation) error {
	_, err := s.db.Exec(`
		INSERT INTO ticket_conversations (
			id, ticket_id, thread_type, title, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`,
		conv.ID, conv.TicketID, conv.ThreadType, conv.Title, conv.Status, conv.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create conversation: %w", err)
	}
	return nil
}

// GetConversation retrieves a conversation by ID.
func (s *Store) GetConversation(id string) (*kanban.TicketConversation, error) {
	row := s.db.QueryRow(`
		SELECT id, ticket_id, thread_type, title, status, created_at, resolved_at
		FROM ticket_conversations WHERE id = ?
	`, id)

	var conv kanban.TicketConversation
	var resolvedAt sql.NullTime
	var title sql.NullString

	err := row.Scan(
		&conv.ID, &conv.TicketID, &conv.ThreadType, &title, &conv.Status,
		&conv.CreatedAt, &resolvedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get conversation: %w", err)
	}

	if title.Valid {
		conv.Title = title.String
	}
	if resolvedAt.Valid {
		conv.ResolvedAt = resolvedAt.Time
	}

	// Load messages
	messages, err := s.GetConversationMessages(id)
	if err == nil {
		conv.Messages = messages
	}

	return &conv, nil
}

// GetConversationsByTicket returns all conversations for a ticket.
func (s *Store) GetConversationsByTicket(ticketID string) ([]kanban.TicketConversation, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, thread_type, title, status, created_at, resolved_at
		FROM ticket_conversations WHERE ticket_id = ? ORDER BY created_at DESC
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query conversations: %w", err)
	}
	defer rows.Close()

	var conversations []kanban.TicketConversation
	for rows.Next() {
		var conv kanban.TicketConversation
		var resolvedAt sql.NullTime
		var title sql.NullString

		err := rows.Scan(
			&conv.ID, &conv.TicketID, &conv.ThreadType, &title, &conv.Status,
			&conv.CreatedAt, &resolvedAt,
		)
		if err != nil {
			return nil, err
		}

		if title.Valid {
			conv.Title = title.String
		}
		if resolvedAt.Valid {
			conv.ResolvedAt = resolvedAt.Time
		}

		conversations = append(conversations, conv)
	}

	return conversations, nil
}

// GetOpenConversationsByTicket returns open (unresolved) conversations for a ticket.
func (s *Store) GetOpenConversationsByTicket(ticketID string) ([]kanban.TicketConversation, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, thread_type, title, status, created_at, resolved_at
		FROM ticket_conversations WHERE ticket_id = ? AND status = 'open' ORDER BY created_at DESC
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query conversations: %w", err)
	}
	defer rows.Close()

	var conversations []kanban.TicketConversation
	for rows.Next() {
		var conv kanban.TicketConversation
		var resolvedAt sql.NullTime
		var title sql.NullString

		err := rows.Scan(
			&conv.ID, &conv.TicketID, &conv.ThreadType, &title, &conv.Status,
			&conv.CreatedAt, &resolvedAt,
		)
		if err != nil {
			return nil, err
		}

		if title.Valid {
			conv.Title = title.String
		}
		if resolvedAt.Valid {
			conv.ResolvedAt = resolvedAt.Time
		}

		conversations = append(conversations, conv)
	}

	return conversations, nil
}

// UpdateConversationStatus updates a conversation's status.
func (s *Store) UpdateConversationStatus(id string, status kanban.ThreadStatus) error {
	var resolvedAt interface{}
	if status == kanban.ThreadStatusResolved {
		resolvedAt = time.Now()
	}

	_, err := s.db.Exec(`
		UPDATE ticket_conversations SET status = ?, resolved_at = ? WHERE id = ?
	`, status, resolvedAt, id)
	return err
}

// --- Conversation Messages ---

// AddConversationMessage adds a message to a conversation thread.
func (s *Store) AddConversationMessage(msg *kanban.ConversationMessage) error {
	metadata, _ := json.Marshal(msg.Metadata)

	_, err := s.db.Exec(`
		INSERT INTO conversation_messages (
			id, conversation_id, agent, message_type, content, metadata, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		msg.ID, msg.ConversationID, msg.Agent, msg.MessageType, msg.Content, metadata, msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to add message: %w", err)
	}
	return nil
}

// GetConversationMessages returns all messages in a conversation.
func (s *Store) GetConversationMessages(conversationID string) ([]kanban.ConversationMessage, error) {
	rows, err := s.db.Query(`
		SELECT id, conversation_id, agent, message_type, content, metadata, created_at
		FROM conversation_messages WHERE conversation_id = ? ORDER BY created_at
	`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []kanban.ConversationMessage
	for rows.Next() {
		var msg kanban.ConversationMessage
		var metadata sql.NullString

		err := rows.Scan(
			&msg.ID, &msg.ConversationID, &msg.Agent, &msg.MessageType,
			&msg.Content, &metadata, &msg.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		if metadata.Valid && metadata.String != "" {
			json.Unmarshal([]byte(metadata.String), &msg.Metadata)
		}

		messages = append(messages, msg)
	}

	// Fetch attachments for each message
	for i := range messages {
		attachments, err := s.GetMessageAttachments(messages[i].ID)
		if err == nil {
			messages[i].Attachments = attachments
		}
	}

	return messages, nil
}

// --- PM Check-ins ---

// AddPMCheckin records a PM check-in.
func (s *Store) AddPMCheckin(checkin *kanban.PMCheckin) error {
	findings, _ := json.Marshal(checkin.Findings)

	_, err := s.db.Exec(`
		INSERT INTO pm_checkins (
			id, ticket_id, conversation_id, checkin_type, summary,
			findings, action_required, resolved, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		checkin.ID, checkin.TicketID, checkin.ConversationID, checkin.CheckinType,
		checkin.Summary, findings, checkin.ActionRequired, checkin.Resolved, checkin.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to add PM checkin: %w", err)
	}
	return nil
}

// GetPMCheckinsByTicket returns all PM check-ins for a ticket.
func (s *Store) GetPMCheckinsByTicket(ticketID string) ([]kanban.PMCheckin, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, conversation_id, checkin_type, summary,
			findings, action_required, resolved, created_at
		FROM pm_checkins WHERE ticket_id = ? ORDER BY created_at DESC
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query PM checkins: %w", err)
	}
	defer rows.Close()

	return scanPMCheckins(rows)
}

// GetUnresolvedPMCheckins returns all unresolved PM check-ins.
func (s *Store) GetUnresolvedPMCheckins() ([]kanban.PMCheckin, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, conversation_id, checkin_type, summary,
			findings, action_required, resolved, created_at
		FROM pm_checkins WHERE resolved = 0 ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query PM checkins: %w", err)
	}
	defer rows.Close()

	return scanPMCheckins(rows)
}

// ResolvePMCheckin marks a PM check-in as resolved.
func (s *Store) ResolvePMCheckin(id string) error {
	_, err := s.db.Exec("UPDATE pm_checkins SET resolved = 1 WHERE id = ?", id)
	return err
}

// GetLastPMCheckin returns the most recent PM check-in for a ticket.
func (s *Store) GetLastPMCheckin(ticketID string) (*kanban.PMCheckin, error) {
	row := s.db.QueryRow(`
		SELECT id, ticket_id, conversation_id, checkin_type, summary,
			findings, action_required, resolved, created_at
		FROM pm_checkins WHERE ticket_id = ? ORDER BY created_at DESC LIMIT 1
	`, ticketID)

	var checkin kanban.PMCheckin
	var convID, findings, actionRequired sql.NullString

	err := row.Scan(
		&checkin.ID, &checkin.TicketID, &convID, &checkin.CheckinType,
		&checkin.Summary, &findings, &actionRequired, &checkin.Resolved, &checkin.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if convID.Valid {
		checkin.ConversationID = convID.String
	}
	if findings.Valid && findings.String != "" {
		json.Unmarshal([]byte(findings.String), &checkin.Findings)
	}
	if actionRequired.Valid {
		checkin.ActionRequired = actionRequired.String
	}

	return &checkin, nil
}

func scanPMCheckins(rows *sql.Rows) ([]kanban.PMCheckin, error) {
	var checkins []kanban.PMCheckin
	for rows.Next() {
		var checkin kanban.PMCheckin
		var convID, findings, actionRequired sql.NullString

		err := rows.Scan(
			&checkin.ID, &checkin.TicketID, &convID, &checkin.CheckinType,
			&checkin.Summary, &findings, &actionRequired, &checkin.Resolved, &checkin.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		if convID.Valid {
			checkin.ConversationID = convID.String
		}
		if findings.Valid && findings.String != "" {
			json.Unmarshal([]byte(findings.String), &checkin.Findings)
		}
		if actionRequired.Valid {
			checkin.ActionRequired = actionRequired.String
		}

		checkins = append(checkins, checkin)
	}
	return checkins, nil
}

// --- Recent Agent Runs ---

// GetRecentRuns returns agent runs from the last 24 hours.
func (s *Store) GetRecentRuns() ([]kanban.AgentRun, error) {
	cutoff := time.Now().Add(-24 * time.Hour)
	rows, err := s.db.Query(`
		SELECT id, agent, ticket_id, worktree, started_at, ended_at, status, output
		FROM agent_runs WHERE started_at > ? ORDER BY started_at DESC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent runs: %w", err)
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

	return runs, nil
}

// GetRun retrieves a single agent run by ID.
func (s *Store) GetRun(id string) (*kanban.AgentRun, error) {
	row := s.db.QueryRow(`
		SELECT id, agent, ticket_id, worktree, started_at, ended_at, status, output
		FROM agent_runs WHERE id = ?
	`, id)

	var run kanban.AgentRun
	var endedAt sql.NullTime
	var output sql.NullString

	err := row.Scan(&run.ID, &run.Agent, &run.TicketID, &run.Worktree,
		&run.StartedAt, &endedAt, &run.Status, &output)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get run: %w", err)
	}

	if endedAt.Valid {
		run.EndedAt = endedAt.Time
	}
	if output.Valid {
		run.Output = output.String
	}

	return &run, nil
}

// GetCompletedRunsCount returns the count of completed agent runs.
func (s *Store) GetCompletedRunsCount() int {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM agent_runs WHERE status != 'running'").Scan(&count)
	return count
}

// --- Worktree Pool Management ---

// RegisterWorktree adds a worktree to the global pool.
func (s *Store) RegisterWorktree(entry kanban.WorktreePoolEntry) error {
	_, err := s.db.Exec(`
		INSERT INTO worktree_pool (
			id, ticket_id, branch, path, agent, status, created_at, last_activity
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		entry.ID, entry.TicketID, entry.Branch, entry.Path, entry.Agent,
		entry.Status, entry.CreatedAt, entry.LastActivity,
	)
	if err != nil {
		return fmt.Errorf("failed to register worktree: %w", err)
	}
	return nil
}

// GetWorktreePool returns all worktrees in the pool.
func (s *Store) GetWorktreePool() ([]kanban.WorktreePoolEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, branch, path, agent, status, created_at, last_activity
		FROM worktree_pool ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query worktree pool: %w", err)
	}
	defer rows.Close()

	return scanWorktreePoolEntries(rows)
}

// GetWorktreeByTicket returns the worktree entry for a specific ticket.
func (s *Store) GetWorktreeByTicket(ticketID string) (*kanban.WorktreePoolEntry, error) {
	row := s.db.QueryRow(`
		SELECT id, ticket_id, branch, path, agent, status, created_at, last_activity
		FROM worktree_pool WHERE ticket_id = ?
	`, ticketID)

	var entry kanban.WorktreePoolEntry
	err := row.Scan(
		&entry.ID, &entry.TicketID, &entry.Branch, &entry.Path, &entry.Agent,
		&entry.Status, &entry.CreatedAt, &entry.LastActivity,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}
	return &entry, nil
}

// GetActiveWorktreeCount returns the count of active worktrees.
func (s *Store) GetActiveWorktreeCount() (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM worktree_pool WHERE status = 'active'
	`).Scan(&count)
	return count, err
}

// UpdateWorktreeStatus updates a worktree's status.
func (s *Store) UpdateWorktreeStatus(ticketID string, status kanban.WorktreePoolStatus) error {
	_, err := s.db.Exec(`
		UPDATE worktree_pool SET status = ?, last_activity = ? WHERE ticket_id = ?
	`, status, time.Now(), ticketID)
	return err
}

// UpdateWorktreeActivity updates the last activity timestamp for a worktree.
func (s *Store) UpdateWorktreeActivity(ticketID string) error {
	_, err := s.db.Exec(`
		UPDATE worktree_pool SET last_activity = ? WHERE ticket_id = ?
	`, time.Now(), ticketID)
	return err
}

// RemoveFromPool removes a worktree from the pool.
func (s *Store) RemoveFromPool(ticketID string) error {
	_, err := s.db.Exec("DELETE FROM worktree_pool WHERE ticket_id = ?", ticketID)
	return err
}

// GetWorktreePoolStats returns statistics about the worktree pool.
func (s *Store) GetWorktreePoolStats() (*kanban.WorktreePoolStats, error) {
	stats := &kanban.WorktreePoolStats{}

	// Get active count
	s.db.QueryRow("SELECT COUNT(*) FROM worktree_pool WHERE status = 'active'").Scan(&stats.ActiveCount)

	// Get merging count
	s.db.QueryRow("SELECT COUNT(*) FROM worktree_pool WHERE status = 'merging'").Scan(&stats.MergingCount)

	// Get limit from config
	limitStr, _ := s.GetConfigValue("max_global_worktrees")
	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &stats.Limit)
	} else {
		stats.Limit = 3 // default
	}

	// Calculate available slots
	stats.AvailableSlots = stats.Limit - stats.ActiveCount - stats.MergingCount
	if stats.AvailableSlots < 0 {
		stats.AvailableSlots = 0
	}

	// Count pending tickets (READY status waiting for worktree)
	s.db.QueryRow(`
		SELECT COUNT(*) FROM tickets WHERE status = 'READY'
	`).Scan(&stats.PendingCount)

	return stats, nil
}

func scanWorktreePoolEntries(rows *sql.Rows) ([]kanban.WorktreePoolEntry, error) {
	var entries []kanban.WorktreePoolEntry
	for rows.Next() {
		var e kanban.WorktreePoolEntry
		err := rows.Scan(
			&e.ID, &e.TicketID, &e.Branch, &e.Path, &e.Agent,
			&e.Status, &e.CreatedAt, &e.LastActivity,
		)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// --- Merge Queue ---

// QueueMerge adds a merge operation to the queue.
func (s *Store) QueueMerge(entry kanban.MergeQueueEntry) error {
	_, err := s.db.Exec(`
		INSERT INTO merge_queue (
			id, ticket_id, branch, status, attempts, last_error, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		entry.ID, entry.TicketID, entry.Branch, entry.Status,
		entry.Attempts, entry.LastError, entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to queue merge: %w", err)
	}
	return nil
}

// GetPendingMerges returns all pending merge operations.
func (s *Store) GetPendingMerges() ([]kanban.MergeQueueEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, branch, status, attempts, last_error, created_at, completed_at
		FROM merge_queue WHERE status IN ('pending', 'in_progress') ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query merge queue: %w", err)
	}
	defer rows.Close()

	return scanMergeQueueEntries(rows)
}

// GetMergeQueue returns all merge queue entries.
func (s *Store) GetMergeQueue() ([]kanban.MergeQueueEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, branch, status, attempts, last_error, created_at, completed_at
		FROM merge_queue ORDER BY created_at DESC LIMIT 50
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query merge queue: %w", err)
	}
	defer rows.Close()

	return scanMergeQueueEntries(rows)
}

// GetMergeQueueByStatus returns merge queue entries filtered by status.
func (s *Store) GetMergeQueueByStatus(status kanban.MergeQueueStatus) ([]kanban.MergeQueueEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, branch, status, attempts, last_error, created_at, completed_at
		FROM merge_queue WHERE status = ? ORDER BY created_at DESC LIMIT 50
	`, status)
	if err != nil {
		return nil, fmt.Errorf("failed to query merge queue by status: %w", err)
	}
	defer rows.Close()

	return scanMergeQueueEntries(rows)
}

// GetMergeByTicket returns the merge queue entry for a specific ticket.
func (s *Store) GetMergeByTicket(ticketID string) (*kanban.MergeQueueEntry, error) {
	row := s.db.QueryRow(`
		SELECT id, ticket_id, branch, status, attempts, last_error, created_at, completed_at
		FROM merge_queue WHERE ticket_id = ? ORDER BY created_at DESC LIMIT 1
	`, ticketID)

	var entry kanban.MergeQueueEntry
	var lastError sql.NullString
	var completedAt sql.NullTime

	err := row.Scan(
		&entry.ID, &entry.TicketID, &entry.Branch, &entry.Status,
		&entry.Attempts, &lastError, &entry.CreatedAt, &completedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get merge entry: %w", err)
	}

	if lastError.Valid {
		entry.LastError = lastError.String
	}
	if completedAt.Valid {
		entry.CompletedAt = &completedAt.Time
	}

	return &entry, nil
}

// UpdateMergeStatus updates a merge operation's status.
func (s *Store) UpdateMergeStatus(id string, status kanban.MergeQueueStatus, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE merge_queue SET status = ?, last_error = ?, attempts = attempts + 1 WHERE id = ?
	`, status, errMsg, id)
	return err
}

// CompleteMerge marks a merge as completed.
func (s *Store) CompleteMerge(id string) error {
	_, err := s.db.Exec(`
		UPDATE merge_queue SET status = 'completed', completed_at = ?, last_error = NULL WHERE id = ?
	`, time.Now(), id)
	return err
}

// FailMerge marks a merge as failed after all retries.
func (s *Store) FailMerge(id string, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE merge_queue SET status = 'failed', last_error = ?, completed_at = ? WHERE id = ?
	`, errMsg, time.Now(), id)
	return err
}

func scanMergeQueueEntries(rows *sql.Rows) ([]kanban.MergeQueueEntry, error) {
	var entries []kanban.MergeQueueEntry
	for rows.Next() {
		var e kanban.MergeQueueEntry
		var lastError sql.NullString
		var completedAt sql.NullTime

		err := rows.Scan(
			&e.ID, &e.TicketID, &e.Branch, &e.Status,
			&e.Attempts, &lastError, &e.CreatedAt, &completedAt,
		)
		if err != nil {
			return nil, err
		}

		if lastError.Valid {
			e.LastError = lastError.String
		}
		if completedAt.Valid {
			e.CompletedAt = &completedAt.Time
		}

		entries = append(entries, e)
	}
	return entries, nil
}

// --- Worktree Events ---

// LogWorktreeEvent records a worktree lifecycle event.
func (s *Store) LogWorktreeEvent(event kanban.WorktreeEvent) error {
	_, err := s.db.Exec(`
		INSERT INTO worktree_events (id, ticket_id, event_type, event_data, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, event.ID, event.TicketID, event.EventType, event.EventData, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to log worktree event: %w", err)
	}
	return nil
}

// GetWorktreeEvents returns worktree events for a specific ticket.
func (s *Store) GetWorktreeEvents(ticketID string) ([]kanban.WorktreeEvent, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, event_type, event_data, created_at
		FROM worktree_events WHERE ticket_id = ? ORDER BY created_at
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query worktree events: %w", err)
	}
	defer rows.Close()

	return scanWorktreeEvents(rows)
}

// GetRecentWorktreeEvents returns recent worktree events across all tickets.
func (s *Store) GetRecentWorktreeEvents(limit int) ([]kanban.WorktreeEvent, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, event_type, event_data, created_at
		FROM worktree_events ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query worktree events: %w", err)
	}
	defer rows.Close()

	return scanWorktreeEvents(rows)
}

func scanWorktreeEvents(rows *sql.Rows) ([]kanban.WorktreeEvent, error) {
	var events []kanban.WorktreeEvent
	for rows.Next() {
		var e kanban.WorktreeEvent
		var eventData sql.NullString

		err := rows.Scan(&e.ID, &e.TicketID, &e.EventType, &eventData, &e.CreatedAt)
		if err != nil {
			return nil, err
		}

		if eventData.Valid {
			e.EventData = eventData.String
		}

		events = append(events, e)
	}
	return events, nil
}

// --- Worktree Config ---

// GetWorktreeConfig returns the worktree manager configuration values.
func (s *Store) GetWorktreeConfig() (maxWorktrees int, mergeAfterDevSignoff bool, cleanupOnMerge bool, checkInterval int) {
	// Defaults
	maxWorktrees = 3
	mergeAfterDevSignoff = true
	cleanupOnMerge = false
	checkInterval = 30

	if v, _ := s.GetConfigValue("max_global_worktrees"); v != "" {
		fmt.Sscanf(v, "%d", &maxWorktrees)
	}
	if v, _ := s.GetConfigValue("merge_after_dev_signoff"); v == "false" {
		mergeAfterDevSignoff = false
	}
	if v, _ := s.GetConfigValue("cleanup_worktree_on_merge"); v == "true" {
		cleanupOnMerge = true
	}
	if v, _ := s.GetConfigValue("worktree_check_interval"); v != "" {
		fmt.Sscanf(v, "%d", &checkInterval)
	}

	return
}

// --- Message Attachments ---

// AddAttachment adds an attachment to a conversation message.
func (s *Store) AddAttachment(att *kanban.Attachment) error {
	_, err := s.db.Exec(`
		INSERT INTO message_attachments (
			id, message_id, filename, content_type, size, path, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		att.ID, att.MessageID, att.Filename, att.ContentType, att.Size, att.Path, att.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to add attachment: %w", err)
	}
	return nil
}

// GetMessageAttachments returns all attachments for a message.
func (s *Store) GetMessageAttachments(messageID string) ([]kanban.Attachment, error) {
	rows, err := s.db.Query(`
		SELECT id, message_id, filename, content_type, size, path, created_at
		FROM message_attachments WHERE message_id = ? ORDER BY created_at
	`, messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query attachments: %w", err)
	}
	defer rows.Close()

	var attachments []kanban.Attachment
	for rows.Next() {
		var att kanban.Attachment
		err := rows.Scan(
			&att.ID, &att.MessageID, &att.Filename, &att.ContentType,
			&att.Size, &att.Path, &att.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, att)
	}
	return attachments, nil
}

// GetAttachment returns a single attachment by ID.
func (s *Store) GetAttachment(id string) (*kanban.Attachment, error) {
	row := s.db.QueryRow(`
		SELECT id, message_id, filename, content_type, size, path, created_at
		FROM message_attachments WHERE id = ?
	`, id)

	var att kanban.Attachment
	err := row.Scan(
		&att.ID, &att.MessageID, &att.Filename, &att.ContentType,
		&att.Size, &att.Path, &att.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get attachment: %w", err)
	}
	return &att, nil
}

// DeleteAttachment removes an attachment.
func (s *Store) DeleteAttachment(id string) error {
	_, err := s.db.Exec("DELETE FROM message_attachments WHERE id = ?", id)
	return err
}

// --- Ticket Time Stats ---

// GetRunsByTicket returns all agent runs for a specific ticket.
func (s *Store) GetRunsByTicket(ticketID string) ([]kanban.AgentRun, error) {
	rows, err := s.db.Query(`
		SELECT id, agent, ticket_id, worktree, started_at, ended_at, status, output
		FROM agent_runs WHERE ticket_id = ? ORDER BY started_at
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query runs by ticket: %w", err)
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
	return runs, nil
}

// GetTicketTimeStats calculates timing statistics for a ticket.
func (s *Store) GetTicketTimeStats(ticketID string) (*kanban.TimeStats, error) {
	// Get the ticket for history and creation time
	ticket, found := s.GetTicket(ticketID)
	if !found {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}

	// Get all runs for this ticket
	runs, err := s.GetRunsByTicket(ticketID)
	if err != nil {
		return nil, err
	}

	stats := &kanban.TimeStats{
		StatusDurations: make(map[kanban.Status]time.Duration),
		AgentWorkTimes:  make(map[string]time.Duration),
	}

	now := time.Now()

	// Calculate total work time from agent runs
	for _, run := range runs {
		stats.AgentRunCount++
		var duration time.Duration
		if run.EndedAt.IsZero() {
			// Still running
			duration = now.Sub(run.StartedAt)
		} else {
			duration = run.EndedAt.Sub(run.StartedAt)
		}
		stats.TotalWorkTime += duration
		stats.AgentWorkTimes[run.Agent] += duration

		// Track last activity
		endTime := run.EndedAt
		if endTime.IsZero() {
			endTime = now
		}
		if endTime.After(stats.LastActivityAt) {
			stats.LastActivityAt = endTime
		}
	}

	// Calculate status durations from history
	if len(ticket.History) > 0 {
		for i := 0; i < len(ticket.History); i++ {
			entry := ticket.History[i]
			var endTime time.Time
			if i < len(ticket.History)-1 {
				endTime = ticket.History[i+1].At
			} else {
				endTime = now
			}
			duration := endTime.Sub(entry.At)
			stats.StatusDurations[entry.Status] += duration
		}
	}

	// Calculate idle time (time in waiting statuses)
	idleStatuses := map[kanban.Status]bool{
		kanban.StatusBacklog:      true,
		kanban.StatusApproved:     true,
		kanban.StatusAwaitingUser: true,
		kanban.StatusReady:        true,
		kanban.StatusBlocked:      true,
	}

	for status, duration := range stats.StatusDurations {
		if idleStatuses[status] {
			stats.TotalIdleTime += duration
		}
	}

	// Calculate total cycle time
	if ticket.Status == kanban.StatusDone {
		// Find when it was marked done
		for _, entry := range ticket.History {
			if entry.Status == kanban.StatusDone {
				stats.TotalCycleTime = entry.At.Sub(ticket.CreatedAt)
				break
			}
		}
	} else {
		stats.TotalCycleTime = now.Sub(ticket.CreatedAt)
	}

	// Check if currently idle
	currentStatus := ticket.Status
	if idleStatuses[currentStatus] {
		// Find when it entered this status
		for i := len(ticket.History) - 1; i >= 0; i-- {
			if ticket.History[i].Status == currentStatus {
				stats.IdleSince = ticket.History[i].At
				stats.CurrentIdleTime = now.Sub(stats.IdleSince)
				break
			}
		}
	}

	return stats, nil
}

// --- ADRs (Architecture Decision Records) ---

// CreateADR creates a new Architecture Decision Record.
func (s *Store) CreateADR(adr *kanban.ADR) error {
	_, err := s.db.Exec(`
		INSERT INTO adrs (
			id, title, status, context, decision, consequences,
			iteration_id, superseded_by, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		adr.ID, adr.Title, adr.Status, adr.Context, adr.Decision, adr.Consequences,
		adr.IterationID, adr.SupersededBy, adr.CreatedBy, adr.CreatedAt, adr.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create ADR: %w", err)
	}
	return nil
}

// GetADR retrieves an ADR by ID.
func (s *Store) GetADR(id string) (*kanban.ADR, error) {
	row := s.db.QueryRow(`
		SELECT id, title, status, context, decision, consequences,
			iteration_id, superseded_by, created_by, created_at, updated_at
		FROM adrs WHERE id = ?
	`, id)

	adr, err := scanADR(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ADR: %w", err)
	}

	// Load linked ticket IDs
	ticketIDs, err := s.getADRTicketIDs(id)
	if err == nil {
		adr.TicketIDs = ticketIDs
	}

	return adr, nil
}

// GetAllADRs returns all ADRs.
func (s *Store) GetAllADRs() ([]kanban.ADR, error) {
	rows, err := s.db.Query(`
		SELECT id, title, status, context, decision, consequences,
			iteration_id, superseded_by, created_by, created_at, updated_at
		FROM adrs ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query ADRs: %w", err)
	}
	defer rows.Close()

	return scanADRRows(rows, s)
}

// GetADRsByIteration returns all ADRs for a specific iteration.
func (s *Store) GetADRsByIteration(iterationID string) ([]kanban.ADR, error) {
	rows, err := s.db.Query(`
		SELECT id, title, status, context, decision, consequences,
			iteration_id, superseded_by, created_by, created_at, updated_at
		FROM adrs WHERE iteration_id = ? ORDER BY created_at
	`, iterationID)
	if err != nil {
		return nil, fmt.Errorf("failed to query ADRs by iteration: %w", err)
	}
	defer rows.Close()

	return scanADRRows(rows, s)
}

// GetADRsByStatus returns ADRs filtered by status.
func (s *Store) GetADRsByStatus(status kanban.ADRStatus) ([]kanban.ADR, error) {
	rows, err := s.db.Query(`
		SELECT id, title, status, context, decision, consequences,
			iteration_id, superseded_by, created_by, created_at, updated_at
		FROM adrs WHERE status = ? ORDER BY created_at DESC
	`, status)
	if err != nil {
		return nil, fmt.Errorf("failed to query ADRs by status: %w", err)
	}
	defer rows.Close()

	return scanADRRows(rows, s)
}

// GetADRsByTicket returns all ADRs linked to a specific ticket.
func (s *Store) GetADRsByTicket(ticketID string) ([]kanban.ADR, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.title, a.status, a.context, a.decision, a.consequences,
			a.iteration_id, a.superseded_by, a.created_by, a.created_at, a.updated_at
		FROM adrs a
		INNER JOIN adr_tickets at ON a.id = at.adr_id
		WHERE at.ticket_id = ?
		ORDER BY a.created_at
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query ADRs by ticket: %w", err)
	}
	defer rows.Close()

	return scanADRRows(rows, s)
}

// UpdateADR updates an existing ADR.
func (s *Store) UpdateADR(adr *kanban.ADR) error {
	_, err := s.db.Exec(`
		UPDATE adrs SET
			title = ?, status = ?, context = ?, decision = ?, consequences = ?,
			iteration_id = ?, superseded_by = ?, updated_at = ?
		WHERE id = ?
	`,
		adr.Title, adr.Status, adr.Context, adr.Decision, adr.Consequences,
		adr.IterationID, adr.SupersededBy, time.Now(), adr.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update ADR: %w", err)
	}
	return nil
}

// DeleteADR deletes an ADR.
func (s *Store) DeleteADR(id string) error {
	_, err := s.db.Exec("DELETE FROM adrs WHERE id = ?", id)
	return err
}

// LinkADRToTicket creates a link between an ADR and a ticket.
func (s *Store) LinkADRToTicket(adrID, ticketID string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO adr_tickets (adr_id, ticket_id) VALUES (?, ?)
	`, adrID, ticketID)
	if err != nil {
		return fmt.Errorf("failed to link ADR to ticket: %w", err)
	}
	return nil
}

// UnlinkADRFromTicket removes the link between an ADR and a ticket.
func (s *Store) UnlinkADRFromTicket(adrID, ticketID string) error {
	_, err := s.db.Exec(`
		DELETE FROM adr_tickets WHERE adr_id = ? AND ticket_id = ?
	`, adrID, ticketID)
	return err
}

// getADRTicketIDs returns all ticket IDs linked to an ADR.
func (s *Store) getADRTicketIDs(adrID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT ticket_id FROM adr_tickets WHERE adr_id = ?
	`, adrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ticketIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ticketIDs = append(ticketIDs, id)
	}
	return ticketIDs, nil
}

// GetNextADRNumber returns the next sequential ADR number.
func (s *Store) GetNextADRNumber() (int, error) {
	var maxID sql.NullString
	err := s.db.QueryRow(`
		SELECT MAX(id) FROM adrs WHERE id LIKE 'ADR-%'
	`).Scan(&maxID)
	if err != nil {
		return 1, nil
	}

	if !maxID.Valid || maxID.String == "" {
		return 1, nil
	}

	var num int
	_, err = fmt.Sscanf(maxID.String, "ADR-%d", &num)
	if err != nil {
		return 1, nil
	}
	return num + 1, nil
}

// ADR scan helpers
func scanADR(row *sql.Row) (*kanban.ADR, error) {
	var adr kanban.ADR
	var context, decision, consequences, iterationID, supersededBy, createdBy sql.NullString

	err := row.Scan(
		&adr.ID, &adr.Title, &adr.Status, &context, &decision, &consequences,
		&iterationID, &supersededBy, &createdBy, &adr.CreatedAt, &adr.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if context.Valid {
		adr.Context = context.String
	}
	if decision.Valid {
		adr.Decision = decision.String
	}
	if consequences.Valid {
		adr.Consequences = consequences.String
	}
	if iterationID.Valid {
		adr.IterationID = iterationID.String
	}
	if supersededBy.Valid {
		adr.SupersededBy = supersededBy.String
	}
	if createdBy.Valid {
		adr.CreatedBy = createdBy.String
	}

	return &adr, nil
}

func scanADRRows(rows *sql.Rows, s *Store) ([]kanban.ADR, error) {
	var adrs []kanban.ADR
	for rows.Next() {
		var adr kanban.ADR
		var context, decision, consequences, iterationID, supersededBy, createdBy sql.NullString

		err := rows.Scan(
			&adr.ID, &adr.Title, &adr.Status, &context, &decision, &consequences,
			&iterationID, &supersededBy, &createdBy, &adr.CreatedAt, &adr.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if context.Valid {
			adr.Context = context.String
		}
		if decision.Valid {
			adr.Decision = decision.String
		}
		if consequences.Valid {
			adr.Consequences = consequences.String
		}
		if iterationID.Valid {
			adr.IterationID = iterationID.String
		}
		if supersededBy.Valid {
			adr.SupersededBy = supersededBy.String
		}
		if createdBy.Valid {
			adr.CreatedBy = createdBy.String
		}

		// Load linked ticket IDs
		ticketIDs, err := s.getADRTicketIDs(adr.ID)
		if err == nil {
			adr.TicketIDs = ticketIDs
		}

		adrs = append(adrs, adr)
	}
	return adrs, nil
}

// --- Tags ---

// CreateTag creates a new tag.
func (s *Store) CreateTag(tag *kanban.Tag) error {
	_, err := s.db.Exec(`
		INSERT INTO tags (id, name, type, color, description, created_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, tag.ID, tag.Name, tag.Type, tag.Color, tag.Description)
	if err != nil {
		return fmt.Errorf("failed to create tag: %w", err)
	}
	return nil
}

// GetTag retrieves a tag by ID.
func (s *Store) GetTag(id string) (*kanban.Tag, error) {
	row := s.db.QueryRow(`
		SELECT id, name, type, color, description FROM tags WHERE id = ?
	`, id)

	var tag kanban.Tag
	var color, description sql.NullString

	err := row.Scan(&tag.ID, &tag.Name, &tag.Type, &color, &description)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tag: %w", err)
	}

	if color.Valid {
		tag.Color = color.String
	}
	if description.Valid {
		tag.Description = description.String
	}

	return &tag, nil
}

// GetTagByName retrieves a tag by its unique name.
func (s *Store) GetTagByName(name string) (*kanban.Tag, error) {
	row := s.db.QueryRow(`
		SELECT id, name, type, color, description FROM tags WHERE name = ?
	`, name)

	var tag kanban.Tag
	var color, description sql.NullString

	err := row.Scan(&tag.ID, &tag.Name, &tag.Type, &color, &description)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tag by name: %w", err)
	}

	if color.Valid {
		tag.Color = color.String
	}
	if description.Valid {
		tag.Description = description.String
	}

	return &tag, nil
}

// GetAllTags returns all tags.
func (s *Store) GetAllTags() ([]kanban.Tag, error) {
	rows, err := s.db.Query(`
		SELECT id, name, type, color, description FROM tags ORDER BY type, name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query tags: %w", err)
	}
	defer rows.Close()

	return scanTagRows(rows)
}

// GetTagsByType returns all tags of a specific type (e.g., "epic").
func (s *Store) GetTagsByType(tagType kanban.TagType) ([]kanban.Tag, error) {
	rows, err := s.db.Query(`
		SELECT id, name, type, color, description FROM tags WHERE type = ? ORDER BY name
	`, tagType)
	if err != nil {
		return nil, fmt.Errorf("failed to query tags by type: %w", err)
	}
	defer rows.Close()

	return scanTagRows(rows)
}

// GetTicketTags returns all tags for a specific ticket.
func (s *Store) GetTicketTags(ticketID string) ([]kanban.Tag, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name, t.type, t.color, t.description
		FROM tags t
		INNER JOIN ticket_tags tt ON t.id = tt.tag_id
		WHERE tt.ticket_id = ?
		ORDER BY t.type, t.name
	`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to query ticket tags: %w", err)
	}
	defer rows.Close()

	return scanTagRows(rows)
}

// AddTagToTicket associates a tag with a ticket.
func (s *Store) AddTagToTicket(ticketID, tagID string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO ticket_tags (ticket_id, tag_id) VALUES (?, ?)
	`, ticketID, tagID)
	if err != nil {
		return fmt.Errorf("failed to add tag to ticket: %w", err)
	}
	return nil
}

// RemoveTagFromTicket removes a tag association from a ticket.
func (s *Store) RemoveTagFromTicket(ticketID, tagID string) error {
	_, err := s.db.Exec(`
		DELETE FROM ticket_tags WHERE ticket_id = ? AND tag_id = ?
	`, ticketID, tagID)
	return err
}

// GetTicketsByTag returns all tickets with a specific tag.
func (s *Store) GetTicketsByTag(tagID string) ([]kanban.Ticket, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.title, t.description, t.domain, t.priority, t.type, t.status,
			t.assigned_agent, t.assignee, t.files, t.dependencies, t.acceptance_criteria,
			t.requirements, t.signoffs, t.bugs, t.notes,
			t.worktree_path, t.worktree_branch, t.worktree_active,
			t.conversation, t.parent_id, t.parallel_group,
			t.created_at, t.updated_at
		FROM tickets t
		INNER JOIN ticket_tags tt ON t.id = tt.ticket_id
		WHERE tt.tag_id = ?
		ORDER BY t.priority, t.created_at
	`, tagID)
	if err != nil {
		return nil, fmt.Errorf("failed to query tickets by tag: %w", err)
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

// UpdateTag updates an existing tag.
func (s *Store) UpdateTag(tag *kanban.Tag) error {
	_, err := s.db.Exec(`
		UPDATE tags SET name = ?, type = ?, color = ?, description = ? WHERE id = ?
	`, tag.Name, tag.Type, tag.Color, tag.Description, tag.ID)
	if err != nil {
		return fmt.Errorf("failed to update tag: %w", err)
	}
	return nil
}

// DeleteTag deletes a tag (cascade removes from ticket_tags).
func (s *Store) DeleteTag(id string) error {
	_, err := s.db.Exec("DELETE FROM tags WHERE id = ?", id)
	return err
}

// GetTagTicketCount returns the number of tickets associated with a tag.
func (s *Store) GetTagTicketCount(tagID string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM ticket_tags WHERE tag_id = ?
	`, tagID).Scan(&count)
	return count, err
}

// GetEpicTags is a convenience method to get all epic-type tags.
func (s *Store) GetEpicTags() ([]kanban.Tag, error) {
	return s.GetTagsByType(kanban.TagTypeEpic)
}

func scanTagRows(rows *sql.Rows) ([]kanban.Tag, error) {
	var tags []kanban.Tag
	for rows.Next() {
		var tag kanban.Tag
		var color, description sql.NullString

		err := rows.Scan(&tag.ID, &tag.Name, &tag.Type, &color, &description)
		if err != nil {
			return nil, err
		}

		if color.Valid {
			tag.Color = color.String
		}
		if description.Valid {
			tag.Description = description.String
		}

		tags = append(tags, tag)
	}
	return tags, nil
}
