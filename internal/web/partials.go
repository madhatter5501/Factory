package web

import (
	"net/http"

	"github.com/madhatter5501/Factory/kanban"
)

// partialBoard returns just the board content for htmx refresh.
func (s *Server) partialBoard(w http.ResponseWriter, r *http.Request) {
	tickets, err := s.store.GetAllTickets()
	if err != nil {
		s.logger.Error("Failed to get tickets", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	columns := groupTicketsByStatus(tickets)
	stats := s.store.GetStats()
	runs := s.store.GetActiveRuns()

	data := map[string]interface{}{
		"Columns":    columns,
		"Stats":      stats,
		"ActiveRuns": runs,
	}

	s.render(w, "partials/board_content.html", data)
}

// partialTicket returns a single ticket card for htmx.
func (s *Server) partialTicket(w http.ResponseWriter, r *http.Request) {
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

	s.render(w, "partials/ticket_card.html", ticket)
}

// partialApproveTicket approves a ticket and returns updated card.
func (s *Server) partialApproveTicket(w http.ResponseWriter, r *http.Request) {
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

	// Only approve from AWAITING_USER
	if ticket.Status != kanban.StatusAwaitingUser {
		http.Error(w, "Ticket is not awaiting approval", http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateTicketStatus(id, kanban.StatusReady, "user", "Approved via dashboard"); err != nil {
		s.logger.Error("Failed to approve ticket", "id", id, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Broadcast to all clients
	s.Broadcast("board-update")

	// Return success message
	w.Header().Set("HX-Trigger", "board-update")
	_, _ = w.Write([]byte(`<div class="success-toast">Ticket approved!</div>`))
}
