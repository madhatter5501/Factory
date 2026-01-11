package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/madhatter5501/Factory/kanban"
	"github.com/google/uuid"
)

// apiGetBoard returns the full board state as JSON.
func (s *Server) apiGetBoard(w http.ResponseWriter, r *http.Request) {
	tickets, err := s.store.GetAllTickets()
	if err != nil {
		s.jsonError(w, "Failed to get tickets", http.StatusInternalServerError)
		return
	}

	stats := s.store.GetStats()
	runs := s.store.GetActiveRuns()

	response := map[string]interface{}{
		"tickets":    tickets,
		"stats":      stats,
		"activeRuns": runs,
		"updatedAt":  time.Now(),
	}

	s.jsonResponse(w, response)
}

// apiGetTickets returns a list of tickets, optionally filtered by status.
func (s *Server) apiGetTickets(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")

	var tickets []kanban.Ticket

	if statusFilter != "" {
		tickets = s.store.GetTicketsByStatus(kanban.Status(statusFilter))
	} else {
		var err error
		tickets, err = s.store.GetAllTickets()
		if err != nil {
			s.jsonError(w, "Failed to get tickets", http.StatusInternalServerError)
			return
		}
	}

	s.jsonResponse(w, tickets)
}

// apiGetTicket returns a single ticket by ID.
func (s *Server) apiGetTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	ticket, found := s.store.GetTicket(id)
	if !found {
		s.jsonError(w, "Ticket not found", http.StatusNotFound)
		return
	}

	s.jsonResponse(w, ticket)
}

// CreateTicketRequest is the request body for creating a ticket.
type CreateTicketRequest struct {
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Domain             string   `json:"domain"`
	Priority           int      `json:"priority"`
	Type               string   `json:"type"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
}

// apiCreateTicket creates a new ticket.
func (s *Server) apiCreateTicket(w http.ResponseWriter, r *http.Request) {
	var req CreateTicketRequest

	// Support both JSON and form data
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") || strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			s.jsonError(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		req.Title = r.FormValue("title")
		req.Description = r.FormValue("description")
		req.Domain = r.FormValue("domain")
		req.Type = r.FormValue("type")
		// Parse priority
		if p := r.FormValue("priority"); p != "" {
			fmt.Sscanf(p, "%d", &req.Priority)
		}
		// Parse acceptance criteria array
		req.AcceptanceCriteria = r.Form["criteria[]"]
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
	}

	if req.Title == "" {
		s.jsonError(w, "Title is required", http.StatusBadRequest)
		return
	}

	ticket := &kanban.Ticket{
		ID:                 uuid.New().String(),
		Title:              req.Title,
		Description:        req.Description,
		Domain:             kanban.Domain(req.Domain),
		Priority:           kanban.Priority(req.Priority),
		Type:               req.Type,
		Status:             kanban.StatusBacklog,
		AcceptanceCriteria: req.AcceptanceCriteria,
		Signoffs: kanban.Signoffs{
			Dev:      false,
			QA:       false,
			UX:       false,
			Security: false,
			PM:       false,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := s.store.CreateTicket(ticket); err != nil {
		s.logger.Error("Failed to create ticket", "error", err)
		s.jsonError(w, "Failed to create ticket", http.StatusInternalServerError)
		return
	}

	// Broadcast update
	s.Broadcast("board-update")

	w.WriteHeader(http.StatusCreated)
	s.jsonResponse(w, ticket)
}

// UpdateTicketRequest is the request body for updating a ticket.
type UpdateTicketRequest struct {
	Title              *string           `json:"title,omitempty"`
	Description        *string           `json:"description,omitempty"`
	Domain             *string           `json:"domain,omitempty"`
	Priority           *int              `json:"priority,omitempty"`
	Type               *string           `json:"type,omitempty"`
	Status             *kanban.Status    `json:"status,omitempty"`
	AssignedAgent      *string           `json:"assignedAgent,omitempty"`
	Assignee           *string           `json:"assignee,omitempty"`
	AcceptanceCriteria []string          `json:"acceptanceCriteria,omitempty"`
	Notes              *string           `json:"notes,omitempty"`
	Requirements       *kanban.Requirements `json:"requirements,omitempty"`
}

// apiUpdateTicket updates an existing ticket.
func (s *Server) apiUpdateTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	ticket, found := s.store.GetTicket(id)
	if !found {
		s.jsonError(w, "Ticket not found", http.StatusNotFound)
		return
	}

	var req UpdateTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Track if status is changing for history
	oldStatus := ticket.Status
	statusChanged := false

	// Apply updates
	if req.Title != nil {
		ticket.Title = *req.Title
	}
	if req.Description != nil {
		ticket.Description = *req.Description
	}
	if req.Domain != nil {
		ticket.Domain = kanban.Domain(*req.Domain)
	}
	if req.Priority != nil {
		ticket.Priority = kanban.Priority(*req.Priority)
	}
	if req.Type != nil {
		ticket.Type = *req.Type
	}
	if req.Status != nil && *req.Status != oldStatus {
		statusChanged = true
		ticket.Status = *req.Status
	}
	if req.AssignedAgent != nil {
		ticket.AssignedAgent = *req.AssignedAgent
	}
	if req.Assignee != nil {
		ticket.Assignee = *req.Assignee
	}
	if req.AcceptanceCriteria != nil {
		ticket.AcceptanceCriteria = req.AcceptanceCriteria
	}
	if req.Notes != nil {
		ticket.Notes = *req.Notes
	}
	if req.Requirements != nil {
		ticket.Requirements = req.Requirements
	}

	ticket.UpdatedAt = time.Now()

	// If status changed, use UpdateTicketStatus to record history
	if statusChanged {
		note := "Status updated via API"
		if req.AssignedAgent != nil && *req.AssignedAgent != "" {
			note = "Picked up by " + *req.AssignedAgent
		}
		if err := s.store.UpdateTicketStatus(id, ticket.Status, "system", note); err != nil {
			s.logger.Error("Failed to update ticket status", "id", id, "error", err)
			s.jsonError(w, "Failed to update ticket", http.StatusInternalServerError)
			return
		}
		// Re-fetch to get updated history
		ticket, _ = s.store.GetTicket(id) // Ignore ok, we know it exists
	}

	// Update other fields
	if err := s.store.UpdateTicket(ticket); err != nil {
		s.logger.Error("Failed to update ticket", "id", id, "error", err)
		s.jsonError(w, "Failed to update ticket", http.StatusInternalServerError)
		return
	}

	// Broadcast update
	s.Broadcast("board-update")

	s.jsonResponse(w, ticket)
}

// apiApproveTicket approves a ticket's requirements and moves it to READY.
func (s *Server) apiApproveTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	ticket, found := s.store.GetTicket(id)
	if !found {
		s.jsonError(w, "Ticket not found", http.StatusNotFound)
		return
	}

	// Only allow approval from AWAITING_USER status
	if ticket.Status != kanban.StatusAwaitingUser {
		s.jsonError(w, "Ticket is not awaiting user approval", http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateTicketStatus(id, kanban.StatusReady, "user", "Requirements approved via dashboard"); err != nil {
		s.logger.Error("Failed to approve ticket", "id", id, "error", err)
		s.jsonError(w, "Failed to approve ticket", http.StatusInternalServerError)
		return
	}

	// Broadcast update
	s.Broadcast("board-update")

	s.jsonResponse(w, map[string]string{"status": "approved"})
}

// apiDeleteTicket deletes a ticket.
func (s *Server) apiDeleteTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteTicket(id); err != nil {
		s.logger.Error("Failed to delete ticket", "id", id, "error", err)
		s.jsonError(w, "Failed to delete ticket", http.StatusInternalServerError)
		return
	}

	// Broadcast update
	s.Broadcast("board-update")

	w.WriteHeader(http.StatusNoContent)
}

// apiGetStats returns board statistics.
func (s *Server) apiGetStats(w http.ResponseWriter, r *http.Request) {
	stats := s.store.GetStats()
	s.jsonResponse(w, stats)
}

// apiGetRuns returns active agent runs.
func (s *Server) apiGetRuns(w http.ResponseWriter, r *http.Request) {
	runs := s.store.GetActiveRuns()
	s.jsonResponse(w, runs)
}

// AnswerQuestionRequest is the request body for answering a PM question.
type AnswerQuestionRequest struct {
	QuestionIndex int    `json:"questionIndex"`
	Answer        string `json:"answer"`
}

// apiAnswerQuestion answers a PM question on a ticket.
func (s *Server) apiAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	ticket, found := s.store.GetTicket(id)
	if !found {
		s.jsonError(w, "Ticket not found", http.StatusNotFound)
		return
	}

	var req AnswerQuestionRequest

	// Support both JSON and form data
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			s.jsonError(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		req.Answer = r.FormValue("answer")
		// Parse questionIndex from form
		if idx := r.FormValue("questionIndex"); idx != "" {
			fmt.Sscanf(idx, "%d", &req.QuestionIndex)
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
	}

	if req.Answer == "" {
		s.jsonError(w, "Answer is required", http.StatusBadRequest)
		return
	}

	// Ensure requirements and questions exist
	if ticket.Requirements == nil || ticket.Requirements.Questions == nil {
		s.jsonError(w, "No questions on this ticket", http.StatusBadRequest)
		return
	}

	if req.QuestionIndex < 0 || req.QuestionIndex >= len(ticket.Requirements.Questions) {
		s.jsonError(w, "Invalid question index", http.StatusBadRequest)
		return
	}

	// Set the answer
	ticket.Requirements.Questions[req.QuestionIndex].Answer = req.Answer
	ticket.UpdatedAt = time.Now()

	if err := s.store.UpdateTicket(ticket); err != nil {
		s.logger.Error("Failed to update ticket", "id", id, "error", err)
		s.jsonError(w, "Failed to answer question", http.StatusInternalServerError)
		return
	}

	// Add history entry
	s.store.AddHistoryEntry(id, ticket.Status, "user", "Answered question: "+req.Answer[:min(50, len(req.Answer))]+"...")

	// Broadcast update
	s.Broadcast("board-update")

	s.jsonResponse(w, ticket)
}

// jsonResponse writes a JSON response.
func (s *Server) jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("Failed to encode JSON response", "error", err)
	}
}

// jsonError writes a JSON error response.
func (s *Server) jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
