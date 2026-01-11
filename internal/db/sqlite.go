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

// Close closes the database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}
