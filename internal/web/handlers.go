package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"factory/agents/provider"
	"factory/kanban"

	"github.com/google/uuid"
)

// getGlobalStatusData returns the system health and stats for the global status bar.
// This should be included in all page data to render the persistent header.
func (s *Server) getGlobalStatusData() (systemHealth *kanban.SystemHealth, stats map[kanban.Status]int) {
	tickets, err := s.store.GetAllTickets()
	if err != nil {
		return nil, nil
	}
	systemHealth = kanban.ComputeSystemHealth(tickets)
	stats = s.store.GetStats()
	return systemHealth, stats
}

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

	// Compute human supervisor context for each ticket
	for i := range tickets {
		tickets[i].BlockedReason = tickets[i].ComputeBlockedReason(tickets)
		tickets[i].CreationContext = tickets[i].ComputeCreationContext(tickets)
	}

	// Compute system health
	systemHealth := kanban.ComputeSystemHealth(tickets)

	// Extract unique domains and agents for facet rail
	domainSet := make(map[string]bool)
	agentSet := make(map[string]bool)
	for _, t := range tickets {
		if t.Domain != "" {
			domainSet[string(t.Domain)] = true
		}
		if t.AssignedAgent != "" {
			agentSet[t.AssignedAgent] = true
		}
	}

	domains := make([]string, 0, len(domainSet))
	for d := range domainSet {
		domains = append(domains, d)
	}
	agents := make([]string, 0, len(agentSet))
	for a := range agentSet {
		agents = append(agents, a)
	}

	// Group tickets by status
	columns := groupTicketsByStatus(tickets)

	stats := s.store.GetStats()
	runs := s.store.GetActiveRuns()

	data := map[string]interface{}{
		"Title":        "Factory Dashboard",
		"Columns":      columns,
		"Stats":        stats,
		"ActiveRuns":   runs,
		"SystemHealth": systemHealth,
		"Domains":      domains,
		"Agents":       agents,
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

	// Load tags for this ticket
	if tags, err := s.store.GetTicketTags(id); err == nil {
		ticket.Tags = tags
	}

	// Fetch conversations for this ticket
	conversations, _ := s.store.GetConversationsByTicket(id)
	// Populate messages for each conversation and collect all messages
	var allMessages []kanban.ConversationMessage
	for i := range conversations {
		messages, _ := s.store.GetConversationMessages(conversations[i].ID)
		conversations[i].Messages = messages
		allMessages = append(allMessages, messages...)
	}

	// Fetch PM check-ins for this ticket
	pmCheckins, _ := s.store.GetPMCheckinsByTicket(id)

	// Fetch git provider configuration
	gitProvider, _ := s.store.GetConfigValue("git_provider")
	gitRepoURL, _ := s.store.GetConfigValue("git_repo_url")

	// Fetch time statistics for this ticket
	timeStats, _ := s.store.GetTicketTimeStats(id)

	// Fetch agent runs for this ticket
	agentRuns, _ := s.store.GetRunsByTicket(id)

	// Get global status data for the persistent header
	systemHealth, stats := s.getGlobalStatusData()

	data := map[string]interface{}{
		"Title":         ticket.Title,
		"Ticket":        ticket,
		"Conversations": conversations,
		"AllMessages":   allMessages,
		"PMCheckins":    pmCheckins,
		"GitProvider":   gitProvider,
		"GitRepoURL":    gitRepoURL,
		"TimeStats":     timeStats,
		"AgentRuns":     agentRuns,
		"SystemHealth":  systemHealth,
		"Stats":         stats,
	}

	s.render(w, "ticket.html", data)
}

// handleAgents renders the agents status view.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	runs := s.store.GetActiveRuns()
	recentRuns, _ := s.store.GetRecentRuns()

	// Get provider configurations for each agent type
	providerConfigs, _ := s.store.GetAllAgentProviderConfigs()

	// Build configs map for template
	configsMap := make(map[string]provider.AgentProviderConfig)
	for _, cfg := range providerConfigs {
		configsMap[cfg.AgentType] = cfg
	}

	// Agent type definitions
	agentDefs := []map[string]string{
		{"type": "pm", "name": "PM Agent", "icon": "clipboard", "desc": "Analyzes requirements, asks questions, validates tickets"},
		{"type": "security", "name": "Security Agent", "icon": "shield", "desc": "Reviews code for security issues"},
		{"type": "dev-frontend", "name": "Frontend Dev", "icon": "code", "desc": "Implements frontend features"},
		{"type": "dev-backend", "name": "Backend Dev", "icon": "server", "desc": "Implements backend features"},
		{"type": "dev-infra", "name": "Infra Dev", "icon": "cloud", "desc": "Implements infrastructure"},
		{"type": "qa", "name": "QA Agent", "icon": "flask", "desc": "Tests implementations"},
		{"type": "ux", "name": "UX Agent", "icon": "palette", "desc": "Reviews UX/UI"},
		{"type": "ideas", "name": "Ideas Agent", "icon": "lightbulb", "desc": "Triages and refines ideas"},
	}

	// Get global status data for the persistent header
	systemHealth, stats := s.getGlobalStatusData()

	data := map[string]interface{}{
		"Title":           "Agents",
		"Runs":            runs,
		"RecentRuns":      recentRuns,
		"ProviderConfigs": configsMap,
		"AgentDefs":       agentDefs,
		"SystemHealth":    systemHealth,
		"Stats":           stats,
	}

	s.render(w, "agents.html", data)
}

// handleAgentDetail renders the detail view for a single agent run.
func (s *Server) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing run ID", http.StatusBadRequest)
		return
	}

	run, err := s.store.GetRun(id)
	if err != nil || run == nil {
		http.NotFound(w, r)
		return
	}

	auditEntries, _ := s.store.GetAuditEntriesByRun(id)

	// Get global status data for the persistent header
	systemHealth, stats := s.getGlobalStatusData()

	data := map[string]interface{}{
		"Title":        "Agent Run " + id,
		"Run":          run,
		"AuditEntries": auditEntries,
		"SystemHealth": systemHealth,
		"Stats":        stats,
	}

	s.render(w, "agent_detail.html", data)
}

// SettingsStats contains statistics for the settings page.
type SettingsStats struct {
	TotalTickets  int
	ActiveRuns    int
	CompletedRuns int
	AuditEntries  int
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
	enableAudit, _ := s.store.GetConfigValue("enable_audit_logging")
	pmCheckinInterval, _ := s.store.GetConfigValue("pm_checkin_interval")
	gitProvider, _ := s.store.GetConfigValue("git_provider")
	gitRepoURL, _ := s.store.GetConfigValue("git_repo_url")

	// Get provider configurations
	providerConfigs, _ := s.store.GetAllAgentProviderConfigs()

	// Build provider configs map for template
	providerConfigsMap := make(map[string]provider.AgentProviderConfig)
	for _, cfg := range providerConfigs {
		providerConfigsMap[cfg.AgentType] = cfg
	}

	// Get available providers with API key status
	providers := provider.AllProviders()
	apiKeyStatus := make(map[string]bool)
	for _, p := range providers {
		apiKeyStatus[p.Name] = os.Getenv(p.EnvVar) != ""
	}

	// Agent types for the template
	agentTypes := []string{"pm", "dev-frontend", "dev-backend", "dev-infra", "qa", "ux", "security", "ideas"}

	// Build settings stats
	ticketStats := s.store.GetStats()
	totalTickets := 0
	for _, count := range ticketStats {
		totalTickets += count
	}

	settingsStats := SettingsStats{
		TotalTickets:  totalTickets,
		ActiveRuns:    len(s.store.GetActiveRuns()),
		CompletedRuns: s.store.GetCompletedRunsCount(),
		AuditEntries:  s.store.GetAuditEntryCount(),
	}

	// Get global status data for the persistent header
	systemHealth, stats := s.getGlobalStatusData()

	data := map[string]interface{}{
		"Title": "Settings",
		"Config": map[string]string{
			"worktree_dir":         worktreeDir,
			"max_parallel_agents":  maxParallel,
			"main_branch":          mainBranch,
			"branch_prefix":        branchPrefix,
			"squash_on_merge":      squashOnMerge,
			"require_all_signoffs": requireSignoffs,
			"enable_audit_logging": enableAudit,
			"pm_checkin_interval":  pmCheckinInterval,
			"git_provider":         gitProvider,
			"git_repo_url":         gitRepoURL,
		},
		"Providers":       providers,
		"ProviderConfigs": providerConfigsMap,
		"APIKeyStatus":    apiKeyStatus,
		"AgentTypes":      agentTypes,
		"SettingsStats":   settingsStats,
		"SystemHealth":    systemHealth,
		"Stats":           stats,
	}

	s.render(w, "settings.html", data)
}

// handleNewTicket renders the new ticket form.
func (s *Server) handleNewTicket(w http.ResponseWriter, r *http.Request) {
	// Get global status data for the persistent header
	systemHealth, stats := s.getGlobalStatusData()

	data := map[string]interface{}{
		"Title":        "New Ticket",
		"SystemHealth": systemHealth,
		"Stats":        stats,
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

// --- Attachment Handlers ---

// apiUploadAttachment handles file uploads to a conversation message.
func (s *Server) apiUploadAttachment(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("messageID")
	if messageID == "" {
		http.Error(w, "Missing message ID", http.StatusBadRequest)
		return
	}

	// Parse multipart form (max 10MB)
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Missing file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Validate content type
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Create uploads directory
	uploadsDir := filepath.Join("uploads", "attachments", messageID)
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		s.logger.Error("Failed to create uploads directory", "error", err)
		http.Error(w, "Failed to create uploads directory", http.StatusInternalServerError)
		return
	}

	// Generate unique filename
	attID := uuid.New().String()
	ext := filepath.Ext(header.Filename)
	storedFilename := attID + ext
	filePath := filepath.Join(uploadsDir, storedFilename)

	// Create the file
	dst, err := os.Create(filePath)
	if err != nil {
		s.logger.Error("Failed to create file", "error", err)
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// Copy the uploaded file
	size, err := io.Copy(dst, file)
	if err != nil {
		s.logger.Error("Failed to copy file", "error", err)
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	// Create attachment record
	att := &kanban.Attachment{
		ID:          attID,
		MessageID:   messageID,
		Filename:    header.Filename,
		ContentType: contentType,
		Size:        size,
		Path:        filePath,
		CreatedAt:   time.Now(),
	}

	if err := s.store.AddAttachment(att); err != nil {
		s.logger.Error("Failed to save attachment", "error", err)
		// Clean up the file
		os.Remove(filePath)
		http.Error(w, "Failed to save attachment", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(`{"id":"` + attID + `"}`))
}

// apiGetAttachment serves an attachment file.
func (s *Server) apiGetAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing attachment ID", http.StatusBadRequest)
		return
	}

	att, err := s.store.GetAttachment(id)
	if err != nil {
		s.logger.Error("Failed to get attachment", "error", err)
		http.Error(w, "Failed to get attachment", http.StatusInternalServerError)
		return
	}
	if att == nil {
		http.NotFound(w, r)
		return
	}

	// Open the file
	file, err := os.Open(att.Path)
	if err != nil {
		s.logger.Error("Failed to open attachment file", "error", err, "path", att.Path)
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	// Set headers
	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Disposition", "inline; filename=\""+att.Filename+"\"")

	// Serve the file
	io.Copy(w, file)
}

// --- ADR (Architecture Decision Records) API Handlers ---

// apiGetADRs returns all ADRs, optionally filtered by iteration or status.
func (s *Server) apiGetADRs(w http.ResponseWriter, r *http.Request) {
	iterationID := r.URL.Query().Get("iteration")
	status := r.URL.Query().Get("status")

	var adrs []kanban.ADR
	var err error

	if iterationID != "" {
		adrs, err = s.store.GetADRsByIteration(iterationID)
	} else if status != "" {
		adrs, err = s.store.GetADRsByStatus(kanban.ADRStatus(status))
	} else {
		adrs, err = s.store.GetAllADRs()
	}

	if err != nil {
		s.logger.Error("Failed to get ADRs", "error", err)
		http.Error(w, "Failed to get ADRs", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, adrs)
}

// apiGetADR returns a single ADR by ID.
func (s *Server) apiGetADR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing ADR ID", http.StatusBadRequest)
		return
	}

	adr, err := s.store.GetADR(id)
	if err != nil {
		s.logger.Error("Failed to get ADR", "error", err)
		http.Error(w, "Failed to get ADR", http.StatusInternalServerError)
		return
	}
	if adr == nil {
		http.NotFound(w, r)
		return
	}

	s.jsonResponse(w, adr)
}

// apiCreateADR creates a new ADR.
func (s *Server) apiCreateADR(w http.ResponseWriter, r *http.Request) {
	var adr kanban.ADR
	if err := json.NewDecoder(r.Body).Decode(&adr); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Generate ID if not provided
	if adr.ID == "" {
		nextNum, _ := s.store.GetNextADRNumber()
		adr.ID = kanban.FormatADRID(nextNum)
	}

	// Set timestamps
	now := time.Now()
	adr.CreatedAt = now
	adr.UpdatedAt = now

	// Default status
	if adr.Status == "" {
		adr.Status = kanban.ADRStatusProposed
	}

	if err := s.store.CreateADR(&adr); err != nil {
		s.logger.Error("Failed to create ADR", "error", err)
		http.Error(w, "Failed to create ADR", http.StatusInternalServerError)
		return
	}

	// Link to tickets if provided
	for _, ticketID := range adr.TicketIDs {
		if err := s.store.LinkADRToTicket(adr.ID, ticketID); err != nil {
			s.logger.Error("Failed to link ADR to ticket", "error", err, "adrID", adr.ID, "ticketID", ticketID)
		}
	}

	w.WriteHeader(http.StatusCreated)
	s.jsonResponse(w, adr)
}

// apiUpdateADR updates an existing ADR.
func (s *Server) apiUpdateADR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing ADR ID", http.StatusBadRequest)
		return
	}

	existing, err := s.store.GetADR(id)
	if err != nil {
		s.logger.Error("Failed to get ADR", "error", err)
		http.Error(w, "Failed to get ADR", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		http.NotFound(w, r)
		return
	}

	var updates kanban.ADR
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Apply updates
	if updates.Title != "" {
		existing.Title = updates.Title
	}
	if updates.Status != "" {
		existing.Status = updates.Status
	}
	if updates.Context != "" {
		existing.Context = updates.Context
	}
	if updates.Decision != "" {
		existing.Decision = updates.Decision
	}
	if updates.Consequences != "" {
		existing.Consequences = updates.Consequences
	}
	if updates.SupersededBy != "" {
		existing.SupersededBy = updates.SupersededBy
	}

	if err := s.store.UpdateADR(existing); err != nil {
		s.logger.Error("Failed to update ADR", "error", err)
		http.Error(w, "Failed to update ADR", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, existing)
}

// apiDeleteADR deletes an ADR.
func (s *Server) apiDeleteADR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing ADR ID", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteADR(id); err != nil {
		s.logger.Error("Failed to delete ADR", "error", err)
		http.Error(w, "Failed to delete ADR", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// apiGetTicketADRs returns all ADRs linked to a specific ticket.
func (s *Server) apiGetTicketADRs(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	if ticketID == "" {
		http.Error(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	adrs, err := s.store.GetADRsByTicket(ticketID)
	if err != nil {
		s.logger.Error("Failed to get ticket ADRs", "error", err)
		http.Error(w, "Failed to get ticket ADRs", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, adrs)
}

// apiLinkADRToTicket creates a link between an ADR and a ticket.
func (s *Server) apiLinkADRToTicket(w http.ResponseWriter, r *http.Request) {
	adrID := r.PathValue("id")
	ticketID := r.PathValue("ticketID")
	if adrID == "" || ticketID == "" {
		http.Error(w, "Missing ADR ID or ticket ID", http.StatusBadRequest)
		return
	}

	if err := s.store.LinkADRToTicket(adrID, ticketID); err != nil {
		s.logger.Error("Failed to link ADR to ticket", "error", err)
		http.Error(w, "Failed to link ADR to ticket", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// apiUnlinkADRFromTicket removes the link between an ADR and a ticket.
func (s *Server) apiUnlinkADRFromTicket(w http.ResponseWriter, r *http.Request) {
	adrID := r.PathValue("id")
	ticketID := r.PathValue("ticketID")
	if adrID == "" || ticketID == "" {
		http.Error(w, "Missing ADR ID or ticket ID", http.StatusBadRequest)
		return
	}

	if err := s.store.UnlinkADRFromTicket(adrID, ticketID); err != nil {
		s.logger.Error("Failed to unlink ADR from ticket", "error", err)
		http.Error(w, "Failed to unlink ADR from ticket", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Tag API Handlers ---

// apiGetTags returns all tags, optionally filtered by type.
func (s *Server) apiGetTags(w http.ResponseWriter, r *http.Request) {
	tagType := r.URL.Query().Get("type")

	var tags []kanban.Tag
	var err error

	if tagType != "" {
		tags, err = s.store.GetTagsByType(kanban.TagType(tagType))
	} else {
		tags, err = s.store.GetAllTags()
	}

	if err != nil {
		s.logger.Error("Failed to get tags", "error", err)
		http.Error(w, "Failed to get tags", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, tags)
}

// apiGetTag returns a single tag by ID.
func (s *Server) apiGetTag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing tag ID", http.StatusBadRequest)
		return
	}

	tag, err := s.store.GetTag(id)
	if err != nil {
		s.logger.Error("Failed to get tag", "error", err)
		http.Error(w, "Failed to get tag", http.StatusInternalServerError)
		return
	}
	if tag == nil {
		http.NotFound(w, r)
		return
	}

	s.jsonResponse(w, tag)
}

// apiCreateTag creates a new tag.
func (s *Server) apiCreateTag(w http.ResponseWriter, r *http.Request) {
	var tag kanban.Tag
	if err := json.NewDecoder(r.Body).Decode(&tag); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if tag.Name == "" {
		http.Error(w, "Tag name is required", http.StatusBadRequest)
		return
	}

	// Generate ID if not provided
	if tag.ID == "" {
		tag.ID = uuid.New().String()
	}

	// Default values
	if tag.Type == "" {
		tag.Type = kanban.TagTypeGeneric
	}
	if tag.Color == "" {
		tag.Color = "#6366f1"
	}

	if err := s.store.CreateTag(&tag); err != nil {
		s.logger.Error("Failed to create tag", "error", err)
		http.Error(w, "Failed to create tag", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	s.jsonResponse(w, tag)
}

// apiUpdateTag updates an existing tag.
func (s *Server) apiUpdateTag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing tag ID", http.StatusBadRequest)
		return
	}

	existing, err := s.store.GetTag(id)
	if err != nil {
		s.logger.Error("Failed to get tag", "error", err)
		http.Error(w, "Failed to get tag", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		http.NotFound(w, r)
		return
	}

	var updates kanban.Tag
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Apply updates
	if updates.Name != "" {
		existing.Name = updates.Name
	}
	if updates.Type != "" {
		existing.Type = updates.Type
	}
	if updates.Color != "" {
		existing.Color = updates.Color
	}
	if updates.Description != "" {
		existing.Description = updates.Description
	}

	if err := s.store.UpdateTag(existing); err != nil {
		s.logger.Error("Failed to update tag", "error", err)
		http.Error(w, "Failed to update tag", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, existing)
}

// apiDeleteTag deletes a tag.
func (s *Server) apiDeleteTag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing tag ID", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteTag(id); err != nil {
		s.logger.Error("Failed to delete tag", "error", err)
		http.Error(w, "Failed to delete tag", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// apiGetTicketsByTag returns all tickets with a specific tag.
func (s *Server) apiGetTicketsByTag(w http.ResponseWriter, r *http.Request) {
	tagID := r.PathValue("id")
	if tagID == "" {
		http.Error(w, "Missing tag ID", http.StatusBadRequest)
		return
	}

	tickets, err := s.store.GetTicketsByTag(tagID)
	if err != nil {
		s.logger.Error("Failed to get tickets by tag", "error", err)
		http.Error(w, "Failed to get tickets by tag", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, tickets)
}

// apiGetTicketTags returns all tags for a specific ticket.
func (s *Server) apiGetTicketTags(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	if ticketID == "" {
		http.Error(w, "Missing ticket ID", http.StatusBadRequest)
		return
	}

	tags, err := s.store.GetTicketTags(ticketID)
	if err != nil {
		s.logger.Error("Failed to get ticket tags", "error", err)
		http.Error(w, "Failed to get ticket tags", http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, tags)
}

// apiAddTagToTicket associates a tag with a ticket.
func (s *Server) apiAddTagToTicket(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	tagID := r.PathValue("tagID")
	if ticketID == "" || tagID == "" {
		http.Error(w, "Missing ticket ID or tag ID", http.StatusBadRequest)
		return
	}

	if err := s.store.AddTagToTicket(ticketID, tagID); err != nil {
		s.logger.Error("Failed to add tag to ticket", "error", err)
		http.Error(w, "Failed to add tag to ticket", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// apiRemoveTagFromTicket removes a tag from a ticket.
func (s *Server) apiRemoveTagFromTicket(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	tagID := r.PathValue("tagID")
	if ticketID == "" || tagID == "" {
		http.Error(w, "Missing ticket ID or tag ID", http.StatusBadRequest)
		return
	}

	if err := s.store.RemoveTagFromTicket(ticketID, tagID); err != nil {
		s.logger.Error("Failed to remove tag from ticket", "error", err)
		http.Error(w, "Failed to remove tag from ticket", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
