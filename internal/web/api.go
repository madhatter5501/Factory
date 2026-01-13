package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"factory/agents/anthropic"
	"factory/agents/provider"
	"factory/kanban"

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
			_, _ = fmt.Sscanf(p, "%d", &req.Priority)
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
	Title              *string              `json:"title,omitempty"`
	Description        *string              `json:"description,omitempty"`
	Domain             *string              `json:"domain,omitempty"`
	Priority           *int                 `json:"priority,omitempty"`
	Type               *string              `json:"type,omitempty"`
	Status             *kanban.Status       `json:"status,omitempty"`
	AssignedAgent      *string              `json:"assignedAgent,omitempty"`
	Assignee           *string              `json:"assignee,omitempty"`
	AcceptanceCriteria []string             `json:"acceptanceCriteria,omitempty"`
	Notes              *string              `json:"notes,omitempty"`
	Requirements       *kanban.Requirements `json:"requirements,omitempty"`
}

// apiUpdateTicket updates an existing ticket.
//
//nolint:gocyclo // API handler with many fields is inherently complex.
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
			_, _ = fmt.Sscanf(idx, "%d", &req.QuestionIndex)
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
	_ = s.store.AddHistoryEntry(id, ticket.Status, "user", "Answered question: "+req.Answer[:min(50, len(req.Answer))]+"...")

	// Broadcast update
	s.Broadcast("board-update")

	s.jsonResponse(w, ticket)
}

// --- Conversation API ---

// apiGetConversations returns conversations for a ticket.
func (s *Server) apiGetConversations(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	if ticketID == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	conversations, err := s.store.GetConversationsByTicket(ticketID)
	if err != nil {
		s.logger.Error("Failed to get conversations", "ticketID", ticketID, "error", err)
		s.jsonError(w, "Failed to get conversations", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, conversations)
}

// apiGetConversation returns a single conversation with its messages.
func (s *Server) apiGetConversation(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("id")
	if convID == "" {
		s.jsonError(w, "Missing conversation ID", http.StatusBadRequest)
		return
	}

	conv, err := s.store.GetConversation(convID)
	if err != nil {
		s.jsonError(w, "Conversation not found", http.StatusNotFound)
		return
	}

	// Load messages for this conversation
	messages, _ := s.store.GetConversationMessages(convID)
	conv.Messages = messages

	s.jsonResponse(w, conv)
}

// CreateConversationRequest is the request body for creating a conversation.
type CreateConversationRequest struct {
	ThreadType string `json:"threadType"`
	Title      string `json:"title"`
}

// apiCreateConversation creates a new conversation thread for a ticket.
func (s *Server) apiCreateConversation(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	if ticketID == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	// Verify ticket exists
	_, found := s.store.GetTicket(ticketID)
	if !found {
		s.jsonError(w, "Ticket not found", http.StatusNotFound)
		return
	}

	var req CreateConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ThreadType == "" {
		req.ThreadType = "dev_discussion"
	}

	conv := &kanban.TicketConversation{
		ID:         uuid.New().String(),
		TicketID:   ticketID,
		ThreadType: kanban.ThreadType(req.ThreadType),
		Title:      req.Title,
		Status:     kanban.ThreadStatusOpen,
		CreatedAt:  time.Now(),
	}

	if err := s.store.CreateConversation(conv); err != nil {
		s.logger.Error("Failed to create conversation", "error", err)
		s.jsonError(w, "Failed to create conversation", http.StatusInternalServerError)
		return
	}

	// Broadcast update
	s.Broadcast("conversation-created")

	w.WriteHeader(http.StatusCreated)
	s.jsonResponse(w, conv)
}

// AddMessageRequest is the request body for adding a message to a conversation.
type AddMessageRequest struct {
	Agent       string `json:"agent"`
	MessageType string `json:"messageType"`
	Content     string `json:"content"`
}

// apiAddMessage adds a message to a conversation thread.
func (s *Server) apiAddMessage(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("id")
	if convID == "" {
		s.jsonError(w, "Missing conversation ID", http.StatusBadRequest)
		return
	}

	// Verify conversation exists
	conv, err := s.store.GetConversation(convID)
	if err != nil {
		s.jsonError(w, "Conversation not found", http.StatusNotFound)
		return
	}

	var req AddMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Content == "" {
		s.jsonError(w, "Message content is required", http.StatusBadRequest)
		return
	}

	if req.Agent == "" {
		req.Agent = "user" //nolint:goconst // Consistent agent name across file
	}
	if req.MessageType == "" {
		req.MessageType = "response"
	}

	msg := &kanban.ConversationMessage{
		ID:             uuid.New().String(),
		ConversationID: convID,
		Agent:          req.Agent,
		MessageType:    kanban.MessageType(req.MessageType),
		Content:        req.Content,
		CreatedAt:      time.Now(),
	}

	if err := s.store.AddConversationMessage(msg); err != nil {
		s.logger.Error("Failed to add message", "error", err)
		s.jsonError(w, "Failed to add message", http.StatusInternalServerError)
		return
	}

	// Broadcast update
	s.Broadcast(fmt.Sprintf("conversation-update:%s", conv.TicketID))

	w.WriteHeader(http.StatusCreated)
	s.jsonResponse(w, msg)
}

// apiResolveConversation marks a conversation as resolved.
func (s *Server) apiResolveConversation(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("id")
	if convID == "" {
		s.jsonError(w, "Missing conversation ID", http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateConversationStatus(convID, kanban.ThreadStatusResolved); err != nil {
		s.logger.Error("Failed to resolve conversation", "error", err)
		s.jsonError(w, "Failed to resolve conversation", http.StatusInternalServerError)
		return
	}

	// Broadcast update
	s.Broadcast("conversation-resolved")

	s.jsonResponse(w, map[string]string{"status": "resolved"})
}

// --- Chat API (simplified user chat with PM response) ---

// apiPostChat handles user chat messages and triggers PM response.
func (s *Server) apiPostChat(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	if ticketID == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	// Parse form content
	if err := r.ParseForm(); err != nil {
		s.jsonError(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	content := r.FormValue("content")
	if content == "" {
		s.jsonError(w, "Message content is required", http.StatusBadRequest)
		return
	}

	// Find or create a user_question conversation for this ticket
	conversations, _ := s.store.GetConversationsByTicket(ticketID)
	var conv *kanban.TicketConversation
	for _, c := range conversations {
		if c.ThreadType == kanban.ThreadTypeUserQuestion && c.Status == kanban.ThreadStatusOpen {
			conv = &c
			break
		}
	}

	// Create conversation if none exists
	if conv == nil {
		newConv := &kanban.TicketConversation{
			ID:         uuid.New().String(),
			TicketID:   ticketID,
			ThreadType: kanban.ThreadTypeUserQuestion,
			Title:      "User Discussion",
			Status:     kanban.ThreadStatusOpen,
			CreatedAt:  time.Now(),
		}
		if err := s.store.CreateConversation(newConv); err != nil {
			s.logger.Error("Failed to create conversation", "error", err)
			s.jsonError(w, "Failed to create conversation", http.StatusInternalServerError)
			return
		}
		conv = newConv
	}

	// Add user message
	userMsg := &kanban.ConversationMessage{
		ID:             uuid.New().String(),
		ConversationID: conv.ID,
		Agent:          "user",
		MessageType:    kanban.MessageTypeQuestion,
		Content:        content,
		CreatedAt:      time.Now(),
	}
	if err := s.store.AddConversationMessage(userMsg); err != nil {
		s.logger.Error("Failed to add message", "error", err)
		s.jsonError(w, "Failed to add message", http.StatusInternalServerError)
		return
	}

	// Broadcast update for SSE
	s.Broadcast(fmt.Sprintf("conversation-update-%s", ticketID))

	// Trigger PM response asynchronously
	go s.generatePMResponse(ticketID, conv.ID, content)

	w.WriteHeader(http.StatusCreated)
	s.jsonResponse(w, userMsg)
}

// generatePMResponse calls the Anthropic API to generate a PM response.
func (s *Server) generatePMResponse(ticketID, convID, userMessage string) {
	// Get ticket context
	ticket, found := s.store.GetTicket(ticketID)
	if !found {
		s.logger.Error("Ticket not found for PM response", "ticketID", ticketID)
		return
	}

	// Get conversation history
	messages, _ := s.store.GetConversationMessages(convID)

	// Build context for PM
	var history string
	for _, m := range messages {
		history += fmt.Sprintf("%s: %s\n", m.Agent, m.Content)
	}

	// Broadcast typing indicator
	s.Broadcast(fmt.Sprintf("pm-typing-%s", ticketID))

	// Generate PM response using Anthropic API
	pmResponse := s.callAnthropicForPMResponse(ticket, history, userMessage)

	// Add PM response as message
	pmMsg := &kanban.ConversationMessage{
		ID:             uuid.New().String(),
		ConversationID: convID,
		Agent:          "PM",
		MessageType:    kanban.MessageTypeResponse,
		Content:        pmResponse,
		CreatedAt:      time.Now(),
	}
	if err := s.store.AddConversationMessage(pmMsg); err != nil {
		s.logger.Error("Failed to add PM response", "error", err)
		return
	}

	// Broadcast update
	s.Broadcast(fmt.Sprintf("conversation-update-%s", ticketID))
}

// callAnthropicForPMResponse makes the actual API call to generate PM response.
func (s *Server) callAnthropicForPMResponse(ticket *kanban.Ticket, history, userMessage string) string {
	// Create Anthropic client
	client, err := anthropic.NewClientFromEnv()
	if err != nil {
		s.logger.Warn("Anthropic API key not set, using placeholder response", "error", err)
		return fmt.Sprintf("Thanks for your message! I'm the PM for ticket \"%s\". "+
			"I've noted your comment and will coordinate with the team. "+
			"Is there anything specific about the implementation you'd like me to clarify?", ticket.Title)
	}

	// Build system prompt
	systemPrompt := fmt.Sprintf(`You are a helpful Product Manager (PM) agent for the Factory development pipeline.
You are responding to a user comment about a specific ticket.

## Ticket Context
- **ID**: %s
- **Title**: %s
- **Status**: %s
- **Description**: %s

## Your Role
- Answer questions about this ticket's implementation, status, or requirements
- Provide helpful clarifications about what the ticket involves
- Coordinate with the development team on the user's behalf
- Be concise but helpful - keep responses under 200 words
- If you don't know something specific, say so honestly

## Conversation History
%s`, ticket.ID, ticket.Title, ticket.Status, ticket.Description, history)

	// Create request using Haiku for fast responses
	req := &anthropic.CreateMessageRequest{
		Model:     anthropic.ModelHaiku35, // Fast model for chat
		MaxTokens: 500,
		System: []anthropic.SystemBlock{
			{Type: "text", Text: systemPrompt},
		},
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: []anthropic.ContentBlock{
					{Type: "text", Text: userMessage},
				},
			},
		},
	}

	// Call API with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.CreateMessage(ctx, req)
	if err != nil {
		s.logger.Error("Failed to get PM response from Anthropic", "error", err)
		return "I apologize, but I'm having trouble processing your message right now. Please try again in a moment."
	}

	return resp.GetText()
}

// apiGetTicketMessages returns all chat messages for a ticket as HTML bubbles.
func (s *Server) apiGetTicketMessages(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	if ticketID == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	// Get all conversations for this ticket
	conversations, _ := s.store.GetConversationsByTicket(ticketID)

	// Collect all messages
	var allMessages []kanban.ConversationMessage
	for _, conv := range conversations {
		messages, _ := s.store.GetConversationMessages(conv.ID)
		allMessages = append(allMessages, messages...)
	}

	// Return as HTML for HTMX swap
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if len(allMessages) == 0 {
		fmt.Fprintf(w, `<div class="chat-empty">
			<div class="chat-empty-icon">&#128172;</div>
			<p>No messages yet</p>
			<p style="font-size: 0.8rem; margin-top: 0.25rem;">Start a conversation with the PM</p>
		</div>`)
		return
	}

	for _, msg := range allMessages {
		bubbleClass := "agent"
		if msg.Agent == "user" {
			bubbleClass = "user"
		}
		avatarClass := getAvatarClass(msg.Agent)
		icon := getAgentIcon(msg.Agent)
		timeAgo := formatTimeAgo(msg.CreatedAt)

		fmt.Fprintf(w, `<div class="chat-bubble-row %s">
			<div class="chat-avatar avatar-%s">%s</div>
			<div class="chat-bubble">
				<div class="chat-bubble-name">%s</div>
				<div class="chat-bubble-content">%s</div>
				<div class="chat-bubble-time">%s</div>
			</div>
		</div>`, bubbleClass, avatarClass, icon, msg.Agent, msg.Content, timeAgo)
	}
}

// getAvatarClass returns the CSS class for an agent's avatar.
//
//nolint:goconst // Case variants are intentional for matching.
func getAvatarClass(agent string) string {
	switch agent {
	case "PM", "pm":
		return "pm"
	case "dev-frontend", "dev-backend", "dev":
		return "dev"
	case "qa", "QA":
		return "qa"
	case "ux", "UX":
		return "ux"
	case "security", "Security":
		return "security"
	case "user", "User":
		return "user"
	default:
		return "dev"
	}
}

// getAgentIcon returns the emoji icon for an agent.
func getAgentIcon(agent string) string {
	switch agent {
	case "PM", "pm":
		return "&#128203;"
	case "dev-frontend":
		return "&#128187;"
	case "dev-backend":
		return "&#9881;"
	case "qa", "QA":
		return "&#129300;"
	case "ux", "UX":
		return "&#127912;"
	case "security", "Security":
		return "&#128274;"
	case "user", "User":
		return "&#128100;"
	default:
		return "&#128187;"
	}
}

// formatTimeAgo returns a human-readable time ago string.
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// --- Audit API ---

// apiGetAuditLog returns audit entries for a run or ticket.
func (s *Server) apiGetAuditLog(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	ticketID := r.URL.Query().Get("ticket_id")

	var entries []kanban.AuditEntry
	var err error

	if runID != "" {
		entries, err = s.store.GetAuditEntriesByRun(runID)
	} else if ticketID != "" {
		entries, err = s.store.GetAuditEntriesByTicket(ticketID)
	} else {
		// Return recent entries
		entries, err = s.store.GetRecentAuditEntries(100)
	}

	if err != nil {
		s.logger.Error("Failed to get audit entries", "error", err)
		s.jsonError(w, "Failed to get audit entries", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, entries)
}

// apiGetRunAudit returns audit entries for a specific agent run.
func (s *Server) apiGetRunAudit(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		s.jsonError(w, "Missing run ID", http.StatusBadRequest)
		return
	}

	entries, err := s.store.GetAuditEntriesByRun(runID)
	if err != nil {
		s.logger.Error("Failed to get audit entries", "runID", runID, "error", err)
		s.jsonError(w, "Failed to get audit entries", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, entries)
}

// --- PM Check-in API ---

// apiGetPMCheckins returns PM check-ins for a ticket.
func (s *Server) apiGetPMCheckins(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	if ticketID == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	checkins, err := s.store.GetPMCheckinsByTicket(ticketID)
	if err != nil {
		s.logger.Error("Failed to get PM check-ins", "ticketID", ticketID, "error", err)
		s.jsonError(w, "Failed to get PM check-ins", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, checkins)
}

// apiGetUnresolvedCheckins returns all unresolved PM check-ins.
func (s *Server) apiGetUnresolvedCheckins(w http.ResponseWriter, r *http.Request) {
	checkins, err := s.store.GetUnresolvedPMCheckins()
	if err != nil {
		s.logger.Error("Failed to get unresolved PM check-ins", "error", err)
		s.jsonError(w, "Failed to get unresolved PM check-ins", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, checkins)
}

// apiResolvePMCheckin marks a PM check-in as resolved.
func (s *Server) apiResolvePMCheckin(w http.ResponseWriter, r *http.Request) {
	checkinID := r.PathValue("id")
	if checkinID == "" {
		s.jsonError(w, "Missing check-in ID", http.StatusBadRequest)
		return
	}

	if err := s.store.ResolvePMCheckin(checkinID); err != nil {
		s.logger.Error("Failed to resolve PM check-in", "error", err)
		s.jsonError(w, "Failed to resolve PM check-in", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, map[string]string{"status": "resolved"})
}

// --- Recent Agent Runs API ---

// apiGetRecentRuns returns recent agent runs (last 24h).
func (s *Server) apiGetRecentRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.GetRecentRuns()
	if err != nil {
		s.logger.Error("Failed to get recent runs", "error", err)
		s.jsonError(w, "Failed to get recent runs", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, runs)
}

// apiGetRunDetail returns details for a specific agent run.
func (s *Server) apiGetRunDetail(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		s.jsonError(w, "Missing run ID", http.StatusBadRequest)
		return
	}

	run, err := s.store.GetRun(runID)
	if err != nil || run == nil {
		s.jsonError(w, "Run not found", http.StatusNotFound)
		return
	}

	// Include audit entries in the response
	auditEntries, _ := s.store.GetAuditEntriesByRun(runID)

	response := map[string]interface{}{
		"run":          run,
		"auditEntries": auditEntries,
	}

	s.jsonResponse(w, response)
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
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		s.logger.Error("Failed to encode JSON error response", "error", err)
	}
}

// --- Worktree Management API ---

// apiGetWorktreePool returns all tracked worktrees in the pool.
func (s *Server) apiGetWorktreePool(w http.ResponseWriter, r *http.Request) {
	pool, err := s.store.GetWorktreePool()
	if err != nil {
		s.logger.Error("Failed to get worktree pool", "error", err)
		s.jsonError(w, "Failed to get worktree pool", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, pool)
}

// apiGetWorktreePoolStats returns the current worktree pool statistics.
func (s *Server) apiGetWorktreePoolStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetWorktreePoolStats()
	if err != nil {
		s.logger.Error("Failed to get worktree pool stats", "error", err)
		s.jsonError(w, "Failed to get worktree pool stats", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, stats)
}

// apiGetMergeQueue returns the merge queue entries.
func (s *Server) apiGetMergeQueue(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")

	var entries []kanban.MergeQueueEntry
	var err error

	if statusFilter != "" {
		entries, err = s.store.GetMergeQueueByStatus(kanban.MergeQueueStatus(statusFilter))
	} else {
		entries, err = s.store.GetMergeQueue()
	}

	if err != nil {
		s.logger.Error("Failed to get merge queue", "error", err)
		s.jsonError(w, "Failed to get merge queue", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, entries)
}

// apiGetWorktreeEvents returns worktree lifecycle events for a ticket.
func (s *Server) apiGetWorktreeEvents(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("ticketID")
	if ticketID == "" {
		s.jsonError(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	events, err := s.store.GetWorktreeEvents(ticketID)
	if err != nil {
		s.logger.Error("Failed to get worktree events", "ticketID", ticketID, "error", err)
		s.jsonError(w, "Failed to get worktree events", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, events)
}

// apiGetRecentWorktreeEvents returns recent worktree events across all tickets.
func (s *Server) apiGetRecentWorktreeEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50 // Default limit
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := fmt.Sscanf(l, "%d", &limit); err != nil || parsed != 1 {
			limit = 50
		}
	}

	events, err := s.store.GetRecentWorktreeEvents(limit)
	if err != nil {
		s.logger.Error("Failed to get recent worktree events", "error", err)
		s.jsonError(w, "Failed to get recent worktree events", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, events)
}

// --- Provider Settings API ---

// apiGetProviderConfigs returns all provider configurations and availability.
func (s *Server) apiGetProviderConfigs(w http.ResponseWriter, r *http.Request) {
	// Get all agent provider configs
	configs, err := s.store.GetAllAgentProviderConfigs()
	if err != nil {
		s.logger.Error("Failed to get provider configs", "error", err)
		s.jsonError(w, "Failed to get provider configs", http.StatusInternalServerError)
		return
	}

	// Get available providers with status
	factory := provider.NewFactory()
	providers := factory.GetAvailableProviders()

	// Check API key availability
	apiKeys := map[string]bool{
		"anthropic": os.Getenv("ANTHROPIC_API_KEY") != "",
		"openai":    os.Getenv("OPENAI_API_KEY") != "",
		"google":    os.Getenv("GOOGLE_API_KEY") != "",
	}

	// Build response with agent labels
	agentLabels := map[string]string{
		"pm":           "PM Agent",
		"dev-frontend": "Frontend Dev",
		"dev-backend":  "Backend Dev",
		"dev-infra":    "Infra Dev",
		"qa":           "QA Agent",
		"ux":           "UX Agent",
		"security":     "Security Agent",
		"ideas":        "Ideas/Triage",
	}

	// Enrich configs with labels
	type enrichedConfig struct {
		provider.AgentProviderConfig
		Label string `json:"label"`
	}
	enrichedConfigs := make([]enrichedConfig, len(configs))
	for i, cfg := range configs {
		enrichedConfigs[i] = enrichedConfig{
			AgentProviderConfig: cfg,
			Label:               agentLabels[cfg.AgentType],
		}
	}

	s.jsonResponse(w, map[string]interface{}{
		"configs":   enrichedConfigs,
		"providers": providers,
		"apiKeys":   apiKeys,
	})
}

// apiUpdateProviderConfigs updates provider/model for one or more agents.
func (s *Server) apiUpdateProviderConfigs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Configs []struct {
			AgentType string `json:"agent_type"`
			Provider  string `json:"provider"`
			Model     string `json:"model"`
		} `json:"configs"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate and update each config
	for _, config := range req.Configs {
		agentType := config.AgentType
		// Validate provider
		validProviders := map[string]bool{"anthropic": true, "openai": true, "google": true}
		if !validProviders[config.Provider] {
			s.jsonError(w, fmt.Sprintf("Invalid provider: %s", config.Provider), http.StatusBadRequest)
			return
		}

		// Validate model belongs to provider
		if !isValidModelForProvider(config.Provider, config.Model) {
			s.jsonError(w, fmt.Sprintf("Invalid model %s for provider %s", config.Model, config.Provider), http.StatusBadRequest)
			return
		}

		if err := s.store.SetAgentProviderConfig(agentType, config.Provider, config.Model); err != nil {
			s.logger.Error("Failed to update provider config", "agentType", agentType, "error", err)
			s.jsonError(w, "Failed to update config", http.StatusInternalServerError)
			return
		}
	}

	// Broadcast settings update
	s.Broadcast("settings-update")

	s.jsonResponse(w, map[string]string{"status": "updated"})
}

// isValidModelForProvider checks if a model is valid for a provider.
func isValidModelForProvider(providerName, model string) bool {
	validModels := map[string][]string{
		"anthropic": {
			provider.ModelAnthropicSonnet4,
			provider.ModelAnthropicHaiku35,
			provider.ModelAnthropicOpus45,
		},
		"openai": {
			provider.ModelOpenAIGPT4o,
			provider.ModelOpenAIGPT4,
			provider.ModelOpenAIGPT35Turbo,
		},
		"google": {
			provider.ModelGoogleGemini20Flash,
			provider.ModelGoogleGemini15Pro,
			provider.ModelGoogleGemini15Flash,
		},
	}

	models, ok := validModels[providerName]
	if !ok {
		return false
	}

	for _, m := range models {
		if m == model {
			return true
		}
	}
	return false
}

// apiGetAgentSystemPrompt returns the system prompt for a specific agent type.
// Returns both the default prompt (from file) and any custom override (from DB).
func (s *Server) apiGetAgentSystemPrompt(w http.ResponseWriter, r *http.Request) {
	agentType := r.PathValue("agentType")
	if agentType == "" {
		s.jsonError(w, "Missing agent type", http.StatusBadRequest)
		return
	}

	config, err := s.store.GetAgentProviderConfig(agentType)
	if err != nil {
		s.logger.Error("Failed to get agent config", "error", err)
		s.jsonError(w, "Failed to get agent config", http.StatusInternalServerError)
		return
	}

	if config == nil {
		s.jsonError(w, "Agent type not found", http.StatusNotFound)
		return
	}

	// Read the default prompt from the prompts directory
	defaultPrompt := readDefaultPrompt(agentType)

	s.jsonResponse(w, map[string]interface{}{
		"agent_type":     config.AgentType,
		"default_prompt": defaultPrompt,
		"custom_prompt":  config.SystemPrompt, // Empty if no override
		"has_override":   config.SystemPrompt != "",
	})
}

// readDefaultPrompt reads the default system prompt from the prompts directory.
func readDefaultPrompt(agentType string) string {
	// Try to read from the prompts directory
	promptPath := fmt.Sprintf("prompts/%s.md", agentType)
	content, err := os.ReadFile(promptPath)
	if err != nil {
		return "" // No default prompt file found
	}
	return string(content)
}

// apiUpdateAgentSystemPrompt updates the system prompt for a specific agent type.
func (s *Server) apiUpdateAgentSystemPrompt(w http.ResponseWriter, r *http.Request) {
	agentType := r.PathValue("agentType")
	if agentType == "" {
		s.jsonError(w, "Missing agent type", http.StatusBadRequest)
		return
	}

	var req struct {
		SystemPrompt string `json:"system_prompt"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Check if the agent type exists
	config, err := s.store.GetAgentProviderConfig(agentType)
	if err != nil {
		s.logger.Error("Failed to get agent config", "error", err)
		s.jsonError(w, "Failed to get agent config", http.StatusInternalServerError)
		return
	}

	if config == nil {
		s.jsonError(w, "Agent type not found", http.StatusNotFound)
		return
	}

	// Update the system prompt
	if err := s.store.SetAgentSystemPrompt(agentType, req.SystemPrompt); err != nil {
		s.logger.Error("Failed to update system prompt", "agentType", agentType, "error", err)
		s.jsonError(w, "Failed to update system prompt", http.StatusInternalServerError)
		return
	}

	// Broadcast settings update
	s.Broadcast("settings-update")

	s.jsonResponse(w, map[string]string{"status": "updated"})
}

// apiDeleteAgentSystemPrompt clears the custom system prompt, reverting to default.
func (s *Server) apiDeleteAgentSystemPrompt(w http.ResponseWriter, r *http.Request) {
	agentType := r.PathValue("agentType")
	if agentType == "" {
		s.jsonError(w, "Missing agent type", http.StatusBadRequest)
		return
	}

	// Check if the agent type exists
	config, err := s.store.GetAgentProviderConfig(agentType)
	if err != nil {
		s.logger.Error("Failed to get agent config", "error", err)
		s.jsonError(w, "Failed to get agent config", http.StatusInternalServerError)
		return
	}

	if config == nil {
		s.jsonError(w, "Agent type not found", http.StatusNotFound)
		return
	}

	// Clear the system prompt (set to empty string)
	if err := s.store.SetAgentSystemPrompt(agentType, ""); err != nil {
		s.logger.Error("Failed to clear system prompt", "agentType", agentType, "error", err)
		s.jsonError(w, "Failed to clear system prompt", http.StatusInternalServerError)
		return
	}

	// Broadcast settings update
	s.Broadcast("settings-update")

	s.jsonResponse(w, map[string]string{"status": "reverted"})
}

// apiGetOrchestratorStatus returns the current orchestrator status.
func (s *Server) apiGetOrchestratorStatus(w http.ResponseWriter, r *http.Request) {
	status := s.GetOrchestratorStatus()
	s.jsonResponse(w, status)
}

// apiStartOrchestrator starts the orchestrator.
func (s *Server) apiStartOrchestrator(w http.ResponseWriter, r *http.Request) {
	if err := s.StartOrchestrator(); err != nil {
		s.logger.Error("Failed to start orchestrator", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, map[string]interface{}{
		"status":  "started",
		"message": "Orchestrator started successfully",
	})
}

// apiStopOrchestrator stops the orchestrator.
func (s *Server) apiStopOrchestrator(w http.ResponseWriter, r *http.Request) {
	s.StopOrchestrator()
	s.jsonResponse(w, map[string]interface{}{
		"status":  "stopped",
		"message": "Orchestrator stopped",
	})
}
