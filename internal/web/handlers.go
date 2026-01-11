package web

import (
	"net/http"

	"github.com/arctek/factory/kanban"
)

// handleBoard renders the main kanban board view.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tickets, err := s.store.GetAllTickets()
	if err != nil {
		s.logger.Error("Failed to get tickets", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Group tickets by status
	columns := groupTicketsByStatus(tickets)

	stats := s.store.GetStats()
	runs := s.store.GetActiveRuns()

	data := map[string]interface{}{
		"Title":      "Factory Dashboard",
		"Columns":    columns,
		"Stats":      stats,
		"ActiveRuns": runs,
	}

	s.render(w, "board.html", data)
}

// handleTicketDetail renders a single ticket's detail view.
func (s *Server) handleTicketDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	ticket, found := s.store.GetTicket(id)
	if !found {
		http.NotFound(w, r)
		return
	}

	data := map[string]interface{}{
		"Title":  ticket.Title,
		"Ticket": ticket,
	}

	s.render(w, "ticket.html", data)
}

// handleAgents renders the agents status view.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	runs := s.store.GetActiveRuns()

	data := map[string]interface{}{
		"Title": "Agents",
		"Runs":  runs,
	}

	s.render(w, "agents.html", data)
}

// handleSettings renders the settings view.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	// Get config values
	worktreeDir, _ := s.store.GetConfigValue("worktree_dir")
	maxParallel, _ := s.store.GetConfigValue("max_parallel_agents")
	mainBranch, _ := s.store.GetConfigValue("main_branch")
	branchPrefix, _ := s.store.GetConfigValue("branch_prefix")
	squashOnMerge, _ := s.store.GetConfigValue("squash_on_merge")
	requireSignoffs, _ := s.store.GetConfigValue("require_all_signoffs")

	data := map[string]interface{}{
		"Title": "Settings",
		"Config": map[string]string{
			"worktree_dir":         worktreeDir,
			"max_parallel_agents":  maxParallel,
			"main_branch":          mainBranch,
			"branch_prefix":        branchPrefix,
			"squash_on_merge":      squashOnMerge,
			"require_all_signoffs": requireSignoffs,
		},
	}

	s.render(w, "settings.html", data)
}

// handleNewTicket renders the new ticket form.
func (s *Server) handleNewTicket(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title": "New Ticket",
	}

	s.render(w, "new_ticket.html", data)
}

// Column represents a kanban column with its tickets.
type Column struct {
	Status  kanban.Status
	Name    string
	Tickets []kanban.Ticket
}

// groupTicketsByStatus groups tickets into columns by their status.
func groupTicketsByStatus(tickets []kanban.Ticket) []Column {
	// Define column order
	statuses := []kanban.Status{
		kanban.StatusBacklog,
		kanban.StatusApproved,
		kanban.StatusRefining,
		kanban.StatusNeedsExpert,
		kanban.StatusAwaitingUser,
		kanban.StatusReady,
		kanban.StatusInDev,
		kanban.StatusInQA,
		kanban.StatusInUX,
		kanban.StatusInSec,
		kanban.StatusPMReview,
		kanban.StatusDone,
		kanban.StatusBlocked,
	}

	// Group tickets by status
	byStatus := make(map[kanban.Status][]kanban.Ticket)
	for _, t := range tickets {
		byStatus[t.Status] = append(byStatus[t.Status], t)
	}

	// Build columns
	columns := make([]Column, 0, len(statuses))
	for _, status := range statuses {
		columns = append(columns, Column{
			Status:  status,
			Name:    statusName(status),
			Tickets: byStatus[status],
		})
	}

	return columns
}

// statusName returns a human-readable name for a status.
func statusName(status kanban.Status) string {
	names := map[kanban.Status]string{
		kanban.StatusBacklog:      "Backlog",
		kanban.StatusApproved:     "Approved",
		kanban.StatusRefining:     "Refining",
		kanban.StatusNeedsExpert:  "Needs Expert",
		kanban.StatusAwaitingUser: "Awaiting User",
		kanban.StatusReady:        "Ready",
		kanban.StatusInDev:        "In Dev",
		kanban.StatusInQA:         "In QA",
		kanban.StatusInUX:         "In UX",
		kanban.StatusInSec:        "In Security",
		kanban.StatusPMReview:     "PM Review",
		kanban.StatusDone:         "Done",
		kanban.StatusBlocked:      "Blocked",
	}
	if name, ok := names[status]; ok {
		return name
	}
	return string(status)
}
