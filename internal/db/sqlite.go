// Package db provides SQLite-based persistence for the factory.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the SQL database connection.
type DB struct {
	*sql.DB
	path string
}

// Open opens or creates a SQLite database at the given path.
func Open(dbPath string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	d := &DB{DB: db, path: dbPath}

	// Run migrations
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return d, nil
}

// migrate runs database migrations.
func (d *DB) migrate() error {
	// Create migrations table
	_, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get current version
	var version int
	row := d.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")
	if err := row.Scan(&version); err != nil {
		return fmt.Errorf("failed to get migration version: %w", err)
	}

	// Apply migrations
	migrations := []struct {
		version int
		sql     string
	}{
		{1, migration1},
		{2, migration2},
		{3, migration3},
		{4, migration4},
		{5, migration5},
		{6, migration6},
		{7, migration7},
		{8, migration8},
		{9, migration9},
		{10, migration10},
		{11, migration11},
	}

	for _, m := range migrations {
		if m.version <= version {
			continue
		}

		if _, err := d.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %d failed: %w", m.version, err)
		}

		if _, err := d.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
			return fmt.Errorf("failed to record migration %d: %w", m.version, err)
		}
	}

	return nil
}

// Migration 1: Core tables
const migration1 = `
-- Tickets table
CREATE TABLE IF NOT EXISTS tickets (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT,
    domain TEXT,
    priority INTEGER DEFAULT 0,
    type TEXT,
    status TEXT NOT NULL DEFAULT 'BACKLOG',
    assigned_agent TEXT,
    assignee TEXT,
    files TEXT,
    dependencies TEXT,
    acceptance_criteria TEXT,
    requirements TEXT,
    signoffs TEXT,
    bugs TEXT,
    notes TEXT,
    worktree_path TEXT,
    worktree_branch TEXT,
    worktree_active INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Ticket history
CREATE TABLE IF NOT EXISTS ticket_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_id TEXT NOT NULL,
    status TEXT NOT NULL,
    changed_by TEXT,
    note TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ticket_history_ticket_id ON ticket_history(ticket_id);
`

// Migration 2: Agent runs
const migration2 = `
-- Agent runs table
CREATE TABLE IF NOT EXISTS agent_runs (
    id TEXT PRIMARY KEY,
    agent TEXT NOT NULL,
    ticket_id TEXT NOT NULL,
    worktree TEXT,
    started_at DATETIME NOT NULL,
    ended_at DATETIME,
    status TEXT DEFAULT 'running',
    output TEXT,
    error TEXT,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_ticket_id ON agent_runs(ticket_id);
CREATE INDEX IF NOT EXISTS idx_agent_runs_status ON agent_runs(status);
`

// Migration 3: Config table
const migration3 = `
-- Board config
CREATE TABLE IF NOT EXISTS config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Insert default config
INSERT OR IGNORE INTO config (key, value) VALUES
    ('worktree_dir', '.worktrees'),
    ('max_parallel_agents', '3'),
    ('main_branch', 'main'),
    ('branch_prefix', 'feat/'),
    ('squash_on_merge', 'true'),
    ('require_all_signoffs', 'true');

-- Iteration table
CREATE TABLE IF NOT EXISTS iterations (
    id TEXT PRIMARY KEY,
    goal TEXT,
    status TEXT DEFAULT 'active',
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);
`

// Migration 4: PRD conversation and parent-child relationships
const migration4 = `
-- Add conversation column for collaborative PRD discussion (JSON)
ALTER TABLE tickets ADD COLUMN conversation TEXT;

-- Add parent_id for sub-tickets created from PRD breakdown
ALTER TABLE tickets ADD COLUMN parent_id TEXT REFERENCES tickets(id);

-- Add parallel_group for scheduling parallel execution
ALTER TABLE tickets ADD COLUMN parallel_group INTEGER DEFAULT 0;

-- Index for finding sub-tickets by parent
CREATE INDEX IF NOT EXISTS idx_tickets_parent ON tickets(parent_id);

-- Index for finding tickets in the same parallel group
CREATE INDEX IF NOT EXISTS idx_tickets_parallel_group ON tickets(parallel_group);
`

// Migration 5: Audit logging, conversations, and PM check-ins
const migration5 = `
-- Agent command/prompt audit log
CREATE TABLE IF NOT EXISTS agent_audit_log (
    id TEXT PRIMARY KEY,
    run_id TEXT REFERENCES agent_runs(id),
    ticket_id TEXT NOT NULL,
    agent TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_data TEXT,
    token_input INTEGER,
    token_output INTEGER,
    duration_ms INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id)
);

CREATE INDEX IF NOT EXISTS idx_audit_log_ticket ON agent_audit_log(ticket_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_run ON agent_audit_log(run_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_created ON agent_audit_log(created_at);

-- Ticket conversation threads (during development)
CREATE TABLE IF NOT EXISTS ticket_conversations (
    id TEXT PRIMARY KEY,
    ticket_id TEXT NOT NULL,
    thread_type TEXT NOT NULL,
    title TEXT,
    status TEXT DEFAULT 'open',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id)
);

CREATE INDEX IF NOT EXISTS idx_conversations_ticket ON ticket_conversations(ticket_id);
CREATE INDEX IF NOT EXISTS idx_conversations_status ON ticket_conversations(status);

-- Individual messages in conversations
CREATE TABLE IF NOT EXISTS conversation_messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    agent TEXT NOT NULL,
    message_type TEXT NOT NULL,
    content TEXT NOT NULL,
    metadata TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (conversation_id) REFERENCES ticket_conversations(id)
);

CREATE INDEX IF NOT EXISTS idx_messages_conversation ON conversation_messages(conversation_id);

-- PM check-ins during development
CREATE TABLE IF NOT EXISTS pm_checkins (
    id TEXT PRIMARY KEY,
    ticket_id TEXT NOT NULL,
    conversation_id TEXT,
    checkin_type TEXT NOT NULL,
    summary TEXT NOT NULL,
    findings TEXT,
    action_required TEXT,
    resolved INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id),
    FOREIGN KEY (conversation_id) REFERENCES ticket_conversations(id)
);

CREATE INDEX IF NOT EXISTS idx_checkins_ticket ON pm_checkins(ticket_id);
CREATE INDEX IF NOT EXISTS idx_checkins_resolved ON pm_checkins(resolved);

-- Add new config values for audit and PM check-ins
INSERT OR IGNORE INTO config (key, value) VALUES
    ('pm_checkin_interval', '15'),
    ('enable_audit_logging', 'true');
`

// Migration 6: Worktree management and merge queue
const migration6 = `
-- Global worktree pool tracking
CREATE TABLE IF NOT EXISTS worktree_pool (
    id TEXT PRIMARY KEY,
    ticket_id TEXT NOT NULL UNIQUE,
    branch TEXT NOT NULL,
    path TEXT NOT NULL,
    agent TEXT NOT NULL,
    status TEXT DEFAULT 'active',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_activity DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id)
);

CREATE INDEX IF NOT EXISTS idx_worktree_pool_status ON worktree_pool(status);
CREATE INDEX IF NOT EXISTS idx_worktree_pool_ticket ON worktree_pool(ticket_id);

-- Merge queue for async merge operations
CREATE TABLE IF NOT EXISTS merge_queue (
    id TEXT PRIMARY KEY,
    ticket_id TEXT NOT NULL,
    branch TEXT NOT NULL,
    status TEXT DEFAULT 'pending',
    attempts INTEGER DEFAULT 0,
    last_error TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id)
);

CREATE INDEX IF NOT EXISTS idx_merge_queue_status ON merge_queue(status);
CREATE INDEX IF NOT EXISTS idx_merge_queue_ticket ON merge_queue(ticket_id);

-- Worktree lifecycle events for auditing
CREATE TABLE IF NOT EXISTS worktree_events (
    id TEXT PRIMARY KEY,
    ticket_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_data TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id)
);

CREATE INDEX IF NOT EXISTS idx_worktree_events_ticket ON worktree_events(ticket_id);
CREATE INDEX IF NOT EXISTS idx_worktree_events_type ON worktree_events(event_type);

-- Add new config values for worktree management
INSERT OR IGNORE INTO config (key, value) VALUES
    ('max_global_worktrees', '3'),
    ('merge_after_dev_signoff', 'true'),
    ('cleanup_worktree_on_merge', 'false'),
    ('worktree_check_interval', '30');
`

// Migration 7: AI Provider Configuration
const migration7 = `
-- Agent provider configuration (provider and model per agent type)
CREATE TABLE IF NOT EXISTS agent_provider_config (
    agent_type TEXT PRIMARY KEY,
    provider TEXT NOT NULL DEFAULT 'anthropic',
    model TEXT NOT NULL DEFAULT 'claude-sonnet-4-20250514',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Insert default configurations (all agents default to Anthropic Sonnet4)
INSERT OR IGNORE INTO agent_provider_config (agent_type, provider, model) VALUES
    ('pm', 'anthropic', 'claude-sonnet-4-20250514'),
    ('dev-frontend', 'anthropic', 'claude-sonnet-4-20250514'),
    ('dev-backend', 'anthropic', 'claude-sonnet-4-20250514'),
    ('dev-infra', 'anthropic', 'claude-sonnet-4-20250514'),
    ('qa', 'anthropic', 'claude-sonnet-4-20250514'),
    ('ux', 'anthropic', 'claude-sonnet-4-20250514'),
    ('security', 'anthropic', 'claude-sonnet-4-20250514'),
    ('ideas', 'anthropic', 'claude-3-5-haiku-20241022');
`

// Migration 8: Git Provider Configuration
const migration8 = `
-- Add git provider configuration to config table
INSERT OR IGNORE INTO config (key, value) VALUES
    ('git_provider', 'github'),
    ('git_repo_url', '');
`

// Migration 9: Message Attachments
const migration9 = `
-- Message attachments for sign-off reports and discussions
CREATE TABLE IF NOT EXISTS message_attachments (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size INTEGER NOT NULL,
    path TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (message_id) REFERENCES conversation_messages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_attachments_message ON message_attachments(message_id);
`

// Migration 10: Agent System Prompts
const migration10 = `
-- Add system_prompt column to agent_provider_config for custom agent prompts
ALTER TABLE agent_provider_config ADD COLUMN system_prompt TEXT;
`

// Migration 11: ADRs and Tags (N:M relationships for flexible categorization)
const migration11 = `
-- Architecture Decision Records captured during requirement gathering
CREATE TABLE IF NOT EXISTS adrs (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'proposed',
    context TEXT,
    decision TEXT,
    consequences TEXT,
    iteration_id TEXT,
    superseded_by TEXT,
    created_by TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- ADR to Ticket relationship (N:M) - an ADR can reference multiple tickets
CREATE TABLE IF NOT EXISTS adr_tickets (
    adr_id TEXT NOT NULL,
    ticket_id TEXT NOT NULL,
    PRIMARY KEY (adr_id, ticket_id),
    FOREIGN KEY (adr_id) REFERENCES adrs(id) ON DELETE CASCADE,
    FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE
);

-- Tags table for flexible categorization (epics, themes, components, etc.)
CREATE TABLE IF NOT EXISTS tags (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL DEFAULT 'tag',
    color TEXT DEFAULT '#6366f1',
    description TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Ticket to Tag relationship (N:M junction table) - tickets can have multiple tags
CREATE TABLE IF NOT EXISTS ticket_tags (
    ticket_id TEXT NOT NULL,
    tag_id TEXT NOT NULL,
    PRIMARY KEY (ticket_id, tag_id),
    FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE,
    FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
);

-- Indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_adrs_iteration ON adrs(iteration_id);
CREATE INDEX IF NOT EXISTS idx_adrs_status ON adrs(status);
CREATE INDEX IF NOT EXISTS idx_adr_tickets_adr ON adr_tickets(adr_id);
CREATE INDEX IF NOT EXISTS idx_adr_tickets_ticket ON adr_tickets(ticket_id);
CREATE INDEX IF NOT EXISTS idx_tags_type ON tags(type);
CREATE INDEX IF NOT EXISTS idx_ticket_tags_ticket ON ticket_tags(ticket_id);
CREATE INDEX IF NOT EXISTS idx_ticket_tags_tag ON ticket_tags(tag_id);
`

// Close closes the database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}
