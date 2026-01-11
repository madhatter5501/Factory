package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/arctek/factory/kanban"
	"github.com/google/uuid"
)

// WizardData holds the collected data from the wizard steps.
type WizardData struct {
	// Phase 1: Problem Understanding
	RawIdea          string `json:"rawIdea"`
	ProblemStatement string `json:"problemStatement"`
	DesiredOutcome   string `json:"desiredOutcome"`
	Type             string `json:"type"`
	Priority         string `json:"priority"`
	OutOfScope       string `json:"outOfScope"`

	// Phase 2: Technical Discovery
	Domain           string   `json:"domain"`
	Stack            []string `json:"stack"`
	AffectedPaths    string   `json:"affectedPaths"`
	PatternsToFollow string   `json:"patternsToFollow"`
	Integrations     string   `json:"integrations"`

	// Phase 3: Acceptance Criteria
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Performance        string   `json:"performance"`
	Security           []string `json:"security"`
	Accessibility      string   `json:"accessibility"`
	TestingNotes       string   `json:"testingNotes"`

	// Phase 4: Review
	Size        string   `json:"size"`
	Reviewers   []string `json:"reviewers"`
	Uncertainty string   `json:"uncertainty"`
}

// WizardSession tracks a wizard session's state.
type WizardSession struct {
	ID        string     `json:"id"`
	Step      int        `json:"step"`
	Data      WizardData `json:"data"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// wizardSessions stores active wizard sessions (in-memory for simplicity).
var (
	wizardSessions   = make(map[string]*WizardSession)
	wizardSessionsMu sync.RWMutex
)

// handleWizard renders the solutions architect wizard.
func (s *Server) handleWizard(w http.ResponseWriter, r *http.Request) {
	// Check for existing session ID in query params
	sessionID := r.URL.Query().Get("session")

	var session *WizardSession
	if sessionID != "" {
		wizardSessionsMu.RLock()
		session = wizardSessions[sessionID]
		wizardSessionsMu.RUnlock()
	}

	// Create new session if needed
	if session == nil {
		session = &WizardSession{
			ID:        uuid.New().String(),
			Step:      1,
			Data:      WizardData{Priority: "medium", Accessibility: "wcag_aa", Size: "M"},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		wizardSessionsMu.Lock()
		wizardSessions[session.ID] = session
		wizardSessionsMu.Unlock()
	}

	data := map[string]interface{}{
		"Title":     "Solutions Architect Wizard",
		"Step":      session.Step,
		"SessionID": session.ID,
		"Data":      session.Data,
	}

	s.render(w, "wizard.html", data)
}

// apiWizard handles wizard form submissions.
func (s *Server) apiWizard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.jsonError(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sessionID := r.FormValue("session_id")
	action := r.FormValue("action")

	// Get or create session
	wizardSessionsMu.Lock()
	session, exists := wizardSessions[sessionID]
	if !exists {
		session = &WizardSession{
			ID:        sessionID,
			Step:      1,
			Data:      WizardData{Priority: "medium", Accessibility: "wcag_aa", Size: "M"},
			CreatedAt: time.Now(),
		}
		wizardSessions[sessionID] = session
	}
	wizardSessionsMu.Unlock()

	// Update session data from form
	updateWizardDataFromForm(session, r)
	session.UpdatedAt = time.Now()

	// Handle action
	switch action {
	case "back":
		if session.Step > 1 {
			session.Step--
		}
	case "next":
		if session.Step < 4 {
			session.Step++
		}
	case "create":
		// Create the ticket
		ticket := wizardDataToTicket(&session.Data)
		if err := s.store.CreateTicket(ticket); err != nil {
			s.logger.Error("Failed to create ticket from wizard", "error", err)
			s.jsonError(w, "Failed to create ticket", http.StatusInternalServerError)
			return
		}

		// Clean up session
		wizardSessionsMu.Lock()
		delete(wizardSessions, sessionID)
		wizardSessionsMu.Unlock()

		// Broadcast update
		s.Broadcast("board-update")

		// Redirect to board
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Render the wizard page with updated state
	data := map[string]interface{}{
		"Title":     "Solutions Architect Wizard",
		"Step":      session.Step,
		"SessionID": session.ID,
		"Data":      session.Data,
	}

	s.render(w, "wizard.html", data)
}

// updateWizardDataFromForm updates the wizard data from form values.
func updateWizardDataFromForm(session *WizardSession, r *http.Request) {
	d := &session.Data

	// Phase 1 fields
	if v := r.FormValue("raw_idea"); v != "" {
		d.RawIdea = v
	}
	if v := r.FormValue("problem_statement"); v != "" {
		d.ProblemStatement = v
	}
	if v := r.FormValue("desired_outcome"); v != "" {
		d.DesiredOutcome = v
	}
	if v := r.FormValue("ticket_type"); v != "" {
		d.Type = v
	}
	if v := r.FormValue("priority"); v != "" {
		d.Priority = v
	}
	if v := r.FormValue("out_of_scope"); v != "" {
		d.OutOfScope = v
	}

	// Phase 2 fields
	if v := r.FormValue("domain"); v != "" {
		d.Domain = v
	}
	if stack := r.Form["stack[]"]; len(stack) > 0 {
		d.Stack = stack
	}
	if v := r.FormValue("affected_paths"); v != "" {
		d.AffectedPaths = v
	}
	if v := r.FormValue("patterns_to_follow"); v != "" {
		d.PatternsToFollow = v
	}
	if v := r.FormValue("integrations"); v != "" {
		d.Integrations = v
	}

	// Phase 3 fields
	if criteria := r.Form["criteria[]"]; len(criteria) > 0 {
		// Filter out empty strings
		filtered := make([]string, 0, len(criteria))
		for _, c := range criteria {
			if strings.TrimSpace(c) != "" {
				filtered = append(filtered, c)
			}
		}
		d.AcceptanceCriteria = filtered
	}
	if v := r.FormValue("performance"); v != "" {
		d.Performance = v
	}
	if security := r.Form["security[]"]; len(security) > 0 {
		d.Security = security
	}
	if v := r.FormValue("accessibility"); v != "" {
		d.Accessibility = v
	}
	if v := r.FormValue("testing_notes"); v != "" {
		d.TestingNotes = v
	}

	// Phase 4 fields
	if v := r.FormValue("size"); v != "" {
		d.Size = v
	}
	if reviewers := r.Form["reviewers[]"]; len(reviewers) > 0 {
		d.Reviewers = reviewers
	}
	if v := r.FormValue("uncertainty"); v != "" {
		d.Uncertainty = v
	}
}

// wizardDataToTicket converts wizard data to a kanban ticket.
func wizardDataToTicket(d *WizardData) *kanban.Ticket {
	// Map priority string to kanban.Priority
	priority := kanban.Priority(3) // medium default
	switch d.Priority {
	case "critical":
		priority = kanban.Priority(1)
	case "high":
		priority = kanban.Priority(2)
	case "medium":
		priority = kanban.Priority(3)
	case "low":
		priority = kanban.Priority(4)
	}

	// Build description with context
	var descParts []string
	descParts = append(descParts, d.RawIdea)
	if d.DesiredOutcome != "" {
		descParts = append(descParts, "\n\n**Desired Outcome:** "+d.DesiredOutcome)
	}
	if d.ProblemStatement != "" {
		descParts = append(descParts, "\n\n**Who:** "+d.ProblemStatement)
	}
	if d.OutOfScope != "" {
		descParts = append(descParts, "\n\n**Out of Scope:** "+d.OutOfScope)
	}
	if d.Uncertainty != "" {
		descParts = append(descParts, "\n\n**Uncertainty:** "+d.Uncertainty)
	}

	// Build technical notes
	var techNotes []string
	if len(d.Stack) > 0 {
		techNotes = append(techNotes, "Stack: "+strings.Join(d.Stack, ", "))
	}
	if d.AffectedPaths != "" {
		techNotes = append(techNotes, "Paths: "+d.AffectedPaths)
	}
	if d.PatternsToFollow != "" {
		techNotes = append(techNotes, "Patterns: "+d.PatternsToFollow)
	}
	if d.Integrations != "" {
		techNotes = append(techNotes, "Integrations: "+d.Integrations)
	}

	// Add constraints to technical notes (no separate Constraints type in kanban)
	if d.Performance != "" {
		techNotes = append(techNotes, "Performance: "+d.Performance)
	}
	if len(d.Security) > 0 {
		techNotes = append(techNotes, "Security: "+strings.Join(d.Security, ", "))
	}
	if d.Accessibility != "" && d.Accessibility != "none" {
		techNotes = append(techNotes, "Accessibility: "+d.Accessibility)
	}
	if d.TestingNotes != "" {
		techNotes = append(techNotes, "Testing: "+d.TestingNotes)
	}

	// Create title from raw idea (truncated)
	title := d.RawIdea
	if len(title) > 100 {
		title = title[:97] + "..."
	}

	ticket := &kanban.Ticket{
		ID:                 uuid.New().String(),
		Title:              title,
		Description:        strings.Join(descParts, ""),
		Domain:             kanban.Domain(d.Domain),
		Priority:           priority,
		Type:               d.Type,
		Status:             kanban.StatusBacklog,
		AcceptanceCriteria: d.AcceptanceCriteria,
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

	// Add requirements with technical notes
	if len(techNotes) > 0 {
		ticket.Requirements = &kanban.Requirements{
			TechnicalNotes: strings.Join(techNotes, "\n"),
		}
	}

	return ticket
}

// cleanupWizardSessions removes stale wizard sessions (call periodically).
func cleanupWizardSessions() {
	wizardSessionsMu.Lock()
	defer wizardSessionsMu.Unlock()

	cutoff := time.Now().Add(-24 * time.Hour)
	for id, session := range wizardSessions {
		if session.UpdatedAt.Before(cutoff) {
			delete(wizardSessions, id)
		}
	}
}

// GetWizardSessionJSON returns a session as JSON for debugging.
func GetWizardSessionJSON(sessionID string) (string, bool) {
	wizardSessionsMu.RLock()
	session, exists := wizardSessions[sessionID]
	wizardSessionsMu.RUnlock()

	if !exists {
		return "", false
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return "", false
	}

	return string(data), true
}
