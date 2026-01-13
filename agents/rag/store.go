// Package rag provides retrieval-augmented generation for the Factory agent.
package rag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Register pure-Go SQLite driver.
)

// VectorStore provides vector storage and similarity search using SQLite.
// Uses a simple but effective approach: store embeddings as JSON arrays and
// compute cosine similarity in Go. For larger scale, this can be upgraded
// to use sqlite-vec extension.
type VectorStore struct {
	db *sql.DB
}

// Chunk represents a stored content chunk with its embedding.
type Chunk struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`    // file path or "expert:domain"
	Content   string    `json:"content"`   // the actual text
	Embedding []float32 `json:"embedding"` // vector representation
	Metadata  Metadata  `json:"metadata"`
	CreatedAt time.Time `json:"created_at"`
}

// Metadata contains additional information about a chunk.
type Metadata struct {
	ChunkType  string   `json:"chunk_type"`  // "code", "pattern", "conversation", "expert"
	Domain     string   `json:"domain"`      // "backend", "frontend", etc.
	Language   string   `json:"language"`    // "go", "csharp", "typescript"
	Tags       []string `json:"tags"`        // searchable tags
	TokenCount int      `json:"token_count"` // approximate token count
}

// SearchResult represents a similarity search result.
type SearchResult struct {
	Chunk      Chunk   `json:"chunk"`
	Similarity float64 `json:"similarity"` // 0.0 to 1.0
}

// NewVectorStore creates a new vector store.
func NewVectorStore(dbPath string) (*VectorStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	store := &VectorStore{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return store, nil
}

// migrate creates the necessary tables.
func (s *VectorStore) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY,
		source TEXT NOT NULL,
		content TEXT NOT NULL,
		embedding TEXT NOT NULL, -- JSON array of floats
		metadata TEXT NOT NULL,  -- JSON object
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source);
	CREATE INDEX IF NOT EXISTS idx_chunks_created ON chunks(created_at);

	-- Full-text search for keyword fallback
	CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		id,
		content,
		source,
		content='chunks',
		content_rowid='rowid'
	);

	-- Triggers to keep FTS in sync
	CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
		INSERT INTO chunks_fts(id, content, source)
		VALUES (new.id, new.content, new.source);
	END;

	CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		DELETE FROM chunks_fts WHERE id = old.id;
	END;

	CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		DELETE FROM chunks_fts WHERE id = old.id;
		INSERT INTO chunks_fts(id, content, source)
		VALUES (new.id, new.content, new.source);
	END;
	`

	_, err := s.db.Exec(schema)
	return err
}

// Store saves a chunk with its embedding.
func (s *VectorStore) Store(ctx context.Context, chunk Chunk) error {
	embeddingJSON, err := json.Marshal(chunk.Embedding)
	if err != nil {
		return fmt.Errorf("failed to marshal embedding: %w", err)
	}

	metadataJSON, err := json.Marshal(chunk.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO chunks (id, source, content, embedding, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, chunk.ID, chunk.Source, chunk.Content, string(embeddingJSON), string(metadataJSON), chunk.CreatedAt)

	return err
}

// StoreBatch saves multiple chunks efficiently.
func (s *VectorStore) StoreBatch(ctx context.Context, chunks []Chunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO chunks (id, source, content, embedding, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, chunk := range chunks {
		embeddingJSON, err := json.Marshal(chunk.Embedding)
		if err != nil {
			return fmt.Errorf("failed to marshal embedding: %w", err)
		}
		metadataJSON, err := json.Marshal(chunk.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}

		_, err = stmt.ExecContext(ctx, chunk.ID, chunk.Source, chunk.Content,
			string(embeddingJSON), string(metadataJSON), chunk.CreatedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Search performs similarity search on the vector store.
func (s *VectorStore) Search(ctx context.Context, queryVec []float32, opts SearchOptions) ([]SearchResult, error) {
	// Build filter clause using parameterized queries (safe from SQL injection)
	var whereClauses []string
	var args []interface{}

	if opts.Domain != "" {
		whereClauses = append(whereClauses, "json_extract(metadata, '$.domain') = ?")
		args = append(args, opts.Domain)
	}
	if opts.ChunkType != "" {
		whereClauses = append(whereClauses, "json_extract(metadata, '$.chunk_type') = ?")
		args = append(args, opts.ChunkType)
	}
	if opts.Source != "" {
		whereClauses = append(whereClauses, "source LIKE ?")
		args = append(args, "%"+opts.Source+"%")
	}

	// Build query - the WHERE clause is built from safe static strings only
	var querySQL string
	if len(whereClauses) > 0 {
		querySQL = "SELECT id, source, content, embedding, metadata, created_at FROM chunks WHERE " +
			strings.Join(whereClauses, " AND ")
	} else {
		querySQL = "SELECT id, source, content, embedding, metadata, created_at FROM chunks"
	}

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult

	for rows.Next() {
		var chunk Chunk
		var embeddingJSON, metadataJSON string

		err := rows.Scan(&chunk.ID, &chunk.Source, &chunk.Content, &embeddingJSON, &metadataJSON, &chunk.CreatedAt)
		if err != nil {
			continue
		}

		if err := json.Unmarshal([]byte(embeddingJSON), &chunk.Embedding); err != nil {
			continue // Skip malformed embeddings
		}
		if err := json.Unmarshal([]byte(metadataJSON), &chunk.Metadata); err != nil {
			continue // Skip malformed metadata
		}

		// Compute cosine similarity
		similarity := cosineSimilarity(queryVec, chunk.Embedding)

		if similarity >= opts.MinSimilarity {
			results = append(results, SearchResult{
				Chunk:      chunk,
				Similarity: similarity,
			})
		}
	}

	// Sort by similarity descending
	sortBySimilarity(results)

	// Apply limit
	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	return results, nil
}

// SearchKeyword performs full-text search as fallback when embeddings aren't available.
func (s *VectorStore) SearchKeyword(ctx context.Context, keywords string, opts SearchOptions) ([]SearchResult, error) {
	query := `
		SELECT c.id, c.source, c.content, c.embedding, c.metadata, c.created_at
		FROM chunks_fts fts
		JOIN chunks c ON fts.id = c.id
		WHERE chunks_fts MATCH ?
		LIMIT ?
	`

	limit := opts.Limit
	if limit == 0 {
		limit = 10
	}

	rows, err := s.db.QueryContext(ctx, query, keywords, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult

	for rows.Next() {
		var chunk Chunk
		var embeddingJSON, metadataJSON string

		err := rows.Scan(&chunk.ID, &chunk.Source, &chunk.Content, &embeddingJSON, &metadataJSON, &chunk.CreatedAt)
		if err != nil {
			continue
		}

		if err := json.Unmarshal([]byte(metadataJSON), &chunk.Metadata); err != nil {
			continue // Skip malformed metadata
		}
		// Don't load embedding for keyword search

		results = append(results, SearchResult{
			Chunk:      chunk,
			Similarity: 0.5, // Fixed similarity for keyword matches
		})
	}

	return results, nil
}

// SearchOptions configures similarity search.
type SearchOptions struct {
	Limit         int     // Max results to return
	MinSimilarity float64 // Minimum similarity threshold (0-1)
	Domain        string  // Filter by domain
	ChunkType     string  // Filter by chunk type
	Source        string  // Filter by source pattern
}

// Delete removes a chunk by ID.
func (s *VectorStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM chunks WHERE id = ?", id)
	return err
}

// DeleteBySource removes all chunks from a source.
func (s *VectorStore) DeleteBySource(ctx context.Context, source string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM chunks WHERE source = ?", source)
	return err
}

// Count returns the total number of chunks.
func (s *VectorStore) Count(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks").Scan(&count)
	return count, err
}

// Close closes the database connection.
func (s *VectorStore) Close() error {
	return s.db.Close()
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// sortBySimilarity sorts results by similarity descending.
func sortBySimilarity(results []SearchResult) {
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Similarity > results[i].Similarity {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
}
