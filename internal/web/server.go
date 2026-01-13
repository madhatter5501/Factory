// Package web provides the HTTP server for the factory dashboard.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"factory"
	"factory/internal/db"
	"factory/kanban"

	"github.com/yuin/goldmark"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server is the factory dashboard web server.
type Server struct {
	store     *db.Store
	db        *db.DB
	templates *template.Template
	logger    *slog.Logger
	server    *http.Server

	// SSE clients
	sseClients   map[chan string]bool
	sseMu        sync.RWMutex
	shutdownOnce sync.Once

	// Orchestrator management
	orchestrator  *factory.Orchestrator
	orchConfig    factory.Config
	orchRepoRoot  string
	orchCtx       context.Context
	orchCancel    context.CancelFunc
	orchRunning   bool
	orchMu        sync.RWMutex
	orchStartedAt time.Time
}

// NewServer creates a new dashboard server (without orchestrator management).
func NewServer(database *db.DB, logger *slog.Logger) (*Server, error) {
	store := db.NewStore(database)

	// Parse templates
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseFS(templatesFS, "templates/*.html", "templates/partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	return &Server{
		store:      store,
		db:         database,
		templates:  tmpl,
		logger:     logger,
		sseClients: make(map[chan string]bool),
	}, nil
}

// NewServerWithOrchestrator creates a dashboard server that can control an orchestrator.
func NewServerWithOrchestrator(database *db.DB, logger *slog.Logger, repoRoot string, config factory.Config) (*Server, error) {
	store := db.NewStore(database)

	// Parse templates
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseFS(templatesFS, "templates/*.html", "templates/partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	return &Server{
		store:        store,
		db:           database,
		templates:    tmpl,
		logger:       logger,
		sseClients:   make(map[chan string]bool),
		orchConfig:   config,
		orchRepoRoot: repoRoot,
	}, nil
}

// StartOrchestrator creates and starts the orchestrator.
func (s *Server) StartOrchestrator() error {
	s.orchMu.Lock()
	defer s.orchMu.Unlock()

	if s.orchRunning {
		return fmt.Errorf("orchestrator is already running")
	}

	// Create new orchestrator
	orch, err := factory.NewOrchestrator(s.orchRepoRoot, s.orchConfig, s.store)
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// Initialize
	if err := orch.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize orchestrator: %w", err)
	}

	// Create context for this run
	ctx, cancel := context.WithCancel(context.Background())
	s.orchestrator = orch
	s.orchCtx = ctx
	s.orchCancel = cancel
	s.orchRunning = true
	s.orchStartedAt = time.Now()

	// Run orchestrator in background
	go func() {
		s.logger.Info("Orchestrator started")
		if err := orch.Run(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("Orchestrator error", "error", err)
		}
		s.logger.Info("Orchestrator stopped")

		// Mark as stopped
		s.orchMu.Lock()
		s.orchRunning = false
		s.orchMu.Unlock()

		// Broadcast status change
		s.Broadcast("orchestrator:stopped")
	}()

	// Broadcast status change
	s.Broadcast("orchestrator:started")

	return nil
}

// StopOrchestrator stops the running orchestrator.
func (s *Server) StopOrchestrator() {
	s.orchMu.Lock()
	defer s.orchMu.Unlock()

	if !s.orchRunning || s.orchCancel == nil {
		return
	}

	s.orchCancel()
	if s.orchestrator != nil {
		s.orchestrator.Stop()
	}
	s.orchRunning = false
}

// OrchestratorStatus represents the orchestrator's current status.
type OrchestratorStatus struct {
	Running   bool             `json:"running"`
	StartedAt time.Time        `json:"startedAt,omitempty"`
	Uptime    string           `json:"uptime,omitempty"`
	Metrics   *factory.Metrics `json:"metrics,omitempty"`
}

// GetOrchestratorStatus returns the current orchestrator status.
func (s *Server) GetOrchestratorStatus() OrchestratorStatus {
	s.orchMu.RLock()
	defer s.orchMu.RUnlock()

	status := OrchestratorStatus{
		Running: s.orchRunning,
	}

	if s.orchRunning && s.orchestrator != nil {
		status.StartedAt = s.orchStartedAt
		status.Uptime = time.Since(s.orchStartedAt).Round(time.Second).String()
		metrics := s.orchestrator.GetMetrics()
		status.Metrics = &metrics
	}

	return status
}

// templateFuncs returns custom template functions.
//
//nolint:gocyclo // Template helper maps are inherently complex.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"contains": func(slice interface{}, item string) bool {
			switch s := slice.(type) {
			case []string:
				for _, v := range s {
					if v == item {
						return true
					}
				}
			}
			return false
		},
		"statusColor": func(status interface{}) string {
			colors := map[string]string{
				"BACKLOG":       "gray",
				"APPROVED":      "blue",
				"REFINING":      "indigo",
				"NEEDS_EXPERT":  "purple",
				"AWAITING_USER": "yellow",
				"READY":         "green",
				"IN_DEV":        "cyan",
				"IN_QA":         "orange",
				"IN_UX":         "pink",
				"IN_SEC":        "red",
				"PM_REVIEW":     "teal",
				"DONE":          "emerald",
				"BLOCKED":       "rose",
			}
			s := fmt.Sprintf("%v", status)
			if c, ok := colors[s]; ok {
				return c
			}
			return "gray"
		},
		"domainIcon": func(domain string) string {
			icons := map[string]string{
				"frontend": "layout",
				"backend":  "server",
				"infra":    "cloud",
				"database": "database",
				"shared":   "share-2",
			}
			if i, ok := icons[domain]; ok {
				return i
			}
			return "file"
		},
		"timeAgo": func(t time.Time) string {
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
		},
		"json": func(v interface{}) string {
			return fmt.Sprintf("%+v", v)
		},
		"truncate": func(n int, s string) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"add": func(a, b int) int {
			return a + b
		},
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "N/A"
			}
			return t.Format("Jan 2, 2006 15:04:05")
		},
		"agentAvatarClass": func(agent string) string {
			// Map agent names to CSS avatar classes
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
		},
		// Markdown rendering.
		"markdown": func(s string) template.HTML {
			var buf bytes.Buffer
			if err := goldmark.Convert([]byte(s), &buf); err != nil {
				return template.HTML(template.HTMLEscapeString(s)) //nolint:gosec // Explicitly escaped
			}
			return template.HTML(buf.String()) //nolint:gosec // goldmark produces safe HTML
		},
		// Lucide icon rendering.
		"icon": func(name string) template.HTML {
			return template.HTML(fmt.Sprintf( //nolint:gosec // Icon names are from internal code
				`<svg class="icon icon-%s"><use href="/static/icons/lucide.svg#%s"></use></svg>`,
				name, name))
		},
		// Create URL-safe slug from ticket title
		"ticketSlug": func(title string) string {
			// Convert to lowercase, replace spaces with hyphens
			slug := strings.ToLower(title)
			slug = strings.ReplaceAll(slug, " ", "-")
			// Remove non-alphanumeric chars except hyphens
			reg := regexp.MustCompile(`[^a-z0-9-]`)
			slug = reg.ReplaceAllString(slug, "")
			// Truncate if too long
			if len(slug) > 50 {
				slug = slug[:50]
			}
			return slug
		},
		// Generate provider-specific branch URL
		"providerBranchURL": func(provider, baseURL, branch string) string {
			if baseURL == "" {
				return ""
			}
			switch strings.ToLower(provider) {
			case "github":
				return fmt.Sprintf("%s/tree/%s", baseURL, branch)
			case "gitlab":
				return fmt.Sprintf("%s/-/tree/%s", baseURL, branch)
			case "bitbucket":
				return fmt.Sprintf("%s/src/%s", baseURL, branch)
			}
			return baseURL
		},
		// Generate git clone command
		"cloneCommand": func(baseURL, branch string) string {
			if baseURL == "" {
				return ""
			}
			// Add .git suffix if not present
			repoURL := baseURL
			if !strings.HasSuffix(repoURL, ".git") {
				repoURL += ".git"
			}
			return fmt.Sprintf("git clone -b %s %s", branch, repoURL)
		},
		// Human-readable thread type name
		"threadTypeName": func(tt kanban.ThreadType) string {
			names := map[kanban.ThreadType]string{
				kanban.ThreadTypeDevDiscussion:   "Dev Discussion",
				kanban.ThreadTypeQAFeedback:      "QA Feedback",
				kanban.ThreadTypePMCheckin:       "PM Check-in",
				kanban.ThreadTypeBlocker:         "Blocker",
				kanban.ThreadTypeUserQuestion:    "User Question",
				kanban.ThreadTypeDevSignoff:      "Development Sign-off",
				kanban.ThreadTypeQASignoff:       "QA Sign-off",
				kanban.ThreadTypeUXSignoff:       "UX Review Sign-off",
				kanban.ThreadTypeSecuritySignoff: "Security Review Sign-off",
				kanban.ThreadTypePMSignoff:       "PM Sign-off",
			}
			if name, ok := names[tt]; ok {
				return name
			}
			return string(tt)
		},
		// Title case string (accepts any type convertible to string).
		"title": func(s any) string {
			str := fmt.Sprintf("%v", s)
			return strings.Title(strings.ToLower(str)) //nolint:staticcheck // Simple ASCII title case is sufficient for UI display
		},
		// Check if string is empty
		"empty": func(s string) bool {
			return s == ""
		},
		// Check if thread type is a sign-off
		"isSignoffThread": func(tt kanban.ThreadType) bool {
			return strings.HasSuffix(string(tt), "_signoff")
		},
		// Get icon for sign-off thread type
		"signoffIcon": func(tt kanban.ThreadType) string {
			icons := map[kanban.ThreadType]string{
				kanban.ThreadTypeDevSignoff:      "code",
				kanban.ThreadTypeQASignoff:       "flask-conical",
				kanban.ThreadTypeUXSignoff:       "palette",
				kanban.ThreadTypeSecuritySignoff: "shield",
				kanban.ThreadTypePMSignoff:       "clipboard-check",
			}
			if icon, ok := icons[tt]; ok {
				return icon
			}
			return "file-check"
		},
		// Get agent name for sign-off thread type (for audit log lookup)
		"signoffAgent": func(tt kanban.ThreadType) string {
			agents := map[kanban.ThreadType]string{
				kanban.ThreadTypeDevSignoff:      "dev",
				kanban.ThreadTypeQASignoff:       "qa",
				kanban.ThreadTypeUXSignoff:       "ux",
				kanban.ThreadTypeSecuritySignoff: "security",
				kanban.ThreadTypePMSignoff:       "PM",
			}
			if agent, ok := agents[tt]; ok {
				return agent
			}
			return ""
		},
		// Get icon for attachment content type
		"attachmentIcon": func(contentType string) string {
			if strings.HasPrefix(contentType, "image/") {
				return "image"
			}
			if contentType == "application/pdf" {
				return "file-text"
			}
			return "file"
		},
		// Format bytes as human readable
		"formatBytes": func(bytes int64) string {
			const unit = 1024
			if bytes < unit {
				return fmt.Sprintf("%d B", bytes)
			}
			div, exp := int64(unit), 0
			for n := bytes / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
		},
		// Parse sign-off report JSON from message content
		"parseSignoffReport": func(content string) *kanban.SignoffReport {
			var report kanban.SignoffReport
			if err := json.Unmarshal([]byte(content), &report); err != nil {
				return nil
			}
			return &report
		},
		// Calculate duration between two times
		"durationBetween": func(start, end time.Time) time.Duration {
			if end.IsZero() || start.IsZero() {
				return 0
			}
			return end.Sub(start)
		},
		// Upper case string
		"upper": func(s string) string {
			return strings.ToUpper(s)
		},
		// Format duration as human-readable string
		"formatDuration": func(d time.Duration) string {
			if d == 0 {
				return "0s"
			}
			hours := int(d.Hours())
			minutes := int(d.Minutes()) % 60
			seconds := int(d.Seconds()) % 60

			if hours > 24 {
				days := hours / 24
				hours = hours % 24
				if hours > 0 {
					return fmt.Sprintf("%dd %dh", days, hours)
				}
				return fmt.Sprintf("%dd", days)
			}
			if hours > 0 {
				if minutes > 0 {
					return fmt.Sprintf("%dh %dm", hours, minutes)
				}
				return fmt.Sprintf("%dh", hours)
			}
			if minutes > 0 {
				if seconds > 0 {
					return fmt.Sprintf("%dm %ds", minutes, seconds)
				}
				return fmt.Sprintf("%dm", minutes)
			}
			return fmt.Sprintf("%ds", seconds)
		},
		// Check if duration is zero
		"durationZero": func(d time.Duration) bool {
			return d == 0
		},
		// Check if time is zero
		"timeZero": func(t time.Time) bool {
			return t.IsZero()
		},
		// Blocked reason icon based on category
		"blockedIcon": func(category string) string {
			icons := map[string]string{
				"dependency": "link",
				"bug":        "bug",
				"policy":     "shield",
				"confidence": "brain",
				"ambiguous":  "help-circle",
				"issue":      "alert-triangle",
				"unknown":    "circle-help",
			}
			if icon, ok := icons[category]; ok {
				return icon
			}
			return "circle-alert"
		},
		// Health status CSS class
		"healthStatusClass": func(status kanban.SystemHealthStatus) string {
			classes := map[kanban.SystemHealthStatus]string{
				kanban.SystemHealthStable:       "health-stable",
				kanban.SystemHealthThrashing:    "health-thrashing",
				kanban.SystemHealthReworking:    "health-reworking",
				kanban.SystemHealthAccumulating: "health-accumulating",
				kanban.SystemHealthStalled:      "health-stalled",
			}
			if class, ok := classes[status]; ok {
				return class
			}
			return "health-unknown"
		},
		// Health status icon
		"healthIcon": func(status kanban.SystemHealthStatus) string {
			icons := map[kanban.SystemHealthStatus]string{
				kanban.SystemHealthStable:       "check-circle",
				kanban.SystemHealthThrashing:    "refresh-cw",
				kanban.SystemHealthReworking:    "rotate-ccw",
				kanban.SystemHealthAccumulating: "layers",
				kanban.SystemHealthStalled:      "alert-octagon",
			}
			if icon, ok := icons[status]; ok {
				return icon
			}
			return "circle"
		},
		// Format creation reason for display
		"creationReasonLabel": func(reason string) string {
			labels := map[string]string{
				"prd_breakdown":  "PRD Breakdown",
				"detected_issue": "Detected Issue",
				"dependency":     "Dependency",
				"user_request":   "User Request",
			}
			if label, ok := labels[reason]; ok {
				return label
			}
			return reason
		},
		// Human-readable status name for PM_REVIEW clarification
		"statusDisplayName": func(status kanban.Status) string {
			names := map[kanban.Status]string{
				kanban.StatusPMReview:     "Awaiting Decision",
				kanban.StatusAwaitingUser: "Requires Confirmation",
				kanban.StatusBlocked:      "Blocked",
				kanban.StatusInDev:        "In Development",
				kanban.StatusInQA:         "In QA",
				kanban.StatusInUX:         "In UX Review",
				kanban.StatusInSec:        "In Security Review",
				kanban.StatusDone:         "Complete",
				kanban.StatusReady:        "Ready",
			}
			if name, ok := names[status]; ok {
				return name
			}
			return string(status)
		},
	}
}

// Start starts the HTTP server.
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()

	// Static files
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Page routes
	mux.HandleFunc("GET /", s.handleBoard)
	mux.HandleFunc("GET /tickets/{id}", s.handleTicketDetail)
	mux.HandleFunc("GET /agents", s.handleAgents)
	mux.HandleFunc("GET /agents/{id}", s.handleAgentDetail)
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("GET /new", s.handleNewTicket)
	mux.HandleFunc("GET /wizard", s.handleWizard)

	// API routes
	mux.HandleFunc("GET /api/board", s.apiGetBoard)
	mux.HandleFunc("GET /api/tickets", s.apiGetTickets)
	mux.HandleFunc("GET /api/tickets/{id}", s.apiGetTicket)
	mux.HandleFunc("POST /api/tickets", s.apiCreateTicket)
	mux.HandleFunc("PATCH /api/tickets/{id}", s.apiUpdateTicket)
	mux.HandleFunc("POST /api/tickets/{id}/ready", s.apiApproveTicket)
	mux.HandleFunc("POST /api/tickets/{id}/answer", s.apiAnswerQuestion)
	mux.HandleFunc("DELETE /api/tickets/{id}", s.apiDeleteTicket)
	mux.HandleFunc("GET /api/stats", s.apiGetStats)
	mux.HandleFunc("GET /api/runs", s.apiGetRuns)
	mux.HandleFunc("POST /api/wizard", s.apiWizard)

	// Conversation API routes
	mux.HandleFunc("GET /api/tickets/{id}/conversations", s.apiGetConversations)
	mux.HandleFunc("POST /api/tickets/{id}/conversations", s.apiCreateConversation)
	mux.HandleFunc("GET /api/conversations/{id}", s.apiGetConversation)
	mux.HandleFunc("POST /api/conversations/{id}/messages", s.apiAddMessage)
	mux.HandleFunc("POST /api/conversations/{id}/resolve", s.apiResolveConversation)

	// Chat API routes (simplified user chat)
	mux.HandleFunc("POST /api/tickets/{id}/chat", s.apiPostChat)
	mux.HandleFunc("GET /api/tickets/{id}/messages", s.apiGetTicketMessages)

	// Audit API routes
	mux.HandleFunc("GET /api/audit", s.apiGetAuditLog)
	mux.HandleFunc("GET /api/runs/{id}/audit", s.apiGetRunAudit)

	// PM Check-in API routes
	mux.HandleFunc("GET /api/tickets/{id}/checkins", s.apiGetPMCheckins)
	mux.HandleFunc("GET /api/checkins/unresolved", s.apiGetUnresolvedCheckins)
	mux.HandleFunc("POST /api/checkins/{id}/resolve", s.apiResolvePMCheckin)

	// Attachment API routes
	mux.HandleFunc("POST /api/messages/{messageID}/attachments", s.apiUploadAttachment)
	mux.HandleFunc("GET /api/attachments/{id}", s.apiGetAttachment)

	// Recent runs API routes
	mux.HandleFunc("GET /api/runs/recent", s.apiGetRecentRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.apiGetRunDetail)

	// Worktree management API routes
	mux.HandleFunc("GET /api/worktrees", s.apiGetWorktreePool)
	mux.HandleFunc("GET /api/worktrees/pool", s.apiGetWorktreePoolStats)
	mux.HandleFunc("GET /api/merge-queue", s.apiGetMergeQueue)
	mux.HandleFunc("GET /api/worktrees/{ticketID}/events", s.apiGetWorktreeEvents)
	mux.HandleFunc("GET /api/worktrees/events/recent", s.apiGetRecentWorktreeEvents)

	// Provider settings API routes
	mux.HandleFunc("GET /api/settings/providers", s.apiGetProviderConfigs)
	mux.HandleFunc("PATCH /api/settings/providers", s.apiUpdateProviderConfigs)
	mux.HandleFunc("GET /api/settings/agents/{agentType}/prompt", s.apiGetAgentSystemPrompt)
	mux.HandleFunc("PATCH /api/settings/agents/{agentType}/prompt", s.apiUpdateAgentSystemPrompt)
	mux.HandleFunc("DELETE /api/settings/agents/{agentType}/prompt", s.apiDeleteAgentSystemPrompt)

	// SSE for real-time updates
	mux.HandleFunc("GET /api/events", s.handleSSE)

	// Orchestrator control API routes
	mux.HandleFunc("GET /api/orchestrator/status", s.apiGetOrchestratorStatus)
	mux.HandleFunc("POST /api/orchestrator/start", s.apiStartOrchestrator)
	mux.HandleFunc("POST /api/orchestrator/stop", s.apiStopOrchestrator)

	// ADRs (Architecture Decision Records)
	mux.HandleFunc("GET /api/adrs", s.apiGetADRs)
	mux.HandleFunc("GET /api/adrs/{id}", s.apiGetADR)
	mux.HandleFunc("POST /api/adrs", s.apiCreateADR)
	mux.HandleFunc("PATCH /api/adrs/{id}", s.apiUpdateADR)
	mux.HandleFunc("DELETE /api/adrs/{id}", s.apiDeleteADR)
	mux.HandleFunc("GET /api/tickets/{id}/adrs", s.apiGetTicketADRs)
	mux.HandleFunc("POST /api/adrs/{id}/tickets/{ticketID}", s.apiLinkADRToTicket)
	mux.HandleFunc("DELETE /api/adrs/{id}/tickets/{ticketID}", s.apiUnlinkADRFromTicket)

	// Tags
	mux.HandleFunc("GET /api/tags", s.apiGetTags)
	mux.HandleFunc("GET /api/tags/{id}", s.apiGetTag)
	mux.HandleFunc("POST /api/tags", s.apiCreateTag)
	mux.HandleFunc("PATCH /api/tags/{id}", s.apiUpdateTag)
	mux.HandleFunc("DELETE /api/tags/{id}", s.apiDeleteTag)
	mux.HandleFunc("GET /api/tags/{id}/tickets", s.apiGetTicketsByTag)
	mux.HandleFunc("GET /api/tickets/{id}/tags", s.apiGetTicketTags)
	mux.HandleFunc("POST /api/tickets/{id}/tags/{tagID}", s.apiAddTagToTicket)
	mux.HandleFunc("DELETE /api/tickets/{id}/tags/{tagID}", s.apiRemoveTagFromTicket)

	// htmx partials
	mux.HandleFunc("GET /partials/board", s.partialBoard)
	mux.HandleFunc("GET /partials/ticket/{id}", s.partialTicket)
	mux.HandleFunc("POST /partials/tickets/{id}/ready", s.partialApproveTicket)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.withLogging(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("Starting dashboard server", "addr", addr)
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		// Close all SSE clients
		s.sseMu.Lock()
		for ch := range s.sseClients {
			close(ch)
			delete(s.sseClients, ch)
		}
		s.sseMu.Unlock()
	})

	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// Broadcast sends an SSE event to all clients.
func (s *Server) Broadcast(event string) {
	s.sseMu.RLock()
	defer s.sseMu.RUnlock()

	for ch := range s.sseClients {
		select {
		case ch <- event:
		default:
			// Client too slow, skip
		}
	}
}

// withLogging wraps a handler with request logging.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Debug("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start))
	})
}

// render executes a template.
func (s *Server) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("Template error", "template", name, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// GetStore returns the database store for external use.
func (s *Server) GetStore() *db.Store {
	return s.store
}
