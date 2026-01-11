// Package web provides the HTTP server for the factory dashboard.
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/madhatter5501/Factory/internal/db"
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
}

// NewServer creates a new dashboard server.
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

// templateFuncs returns custom template functions.
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

	// SSE for real-time updates
	mux.HandleFunc("GET /api/events", s.handleSSE)

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
