package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Indexer processes and indexes content for RAG retrieval.
type Indexer struct {
	store    *VectorStore
	embedder *Embedder
}

// NewIndexer creates a new content indexer.
func NewIndexer(store *VectorStore, embedder *Embedder) *Indexer {
	return &Indexer{
		store:    store,
		embedder: embedder,
	}
}

// IndexExpertPrompts indexes all expert knowledge prompts.
func (idx *Indexer) IndexExpertPrompts(ctx context.Context, promptsDir string) error {
	expertsDir := filepath.Join(promptsDir, "experts")
	entries, err := os.ReadDir(expertsDir)
	if err != nil {
		return fmt.Errorf("failed to read experts directory: %w", err)
	}

	var allChunks []Chunk

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		domain := strings.TrimSuffix(entry.Name(), ".md")
		path := filepath.Join(expertsDir, entry.Name())

		chunks, err := idx.processExpertPrompt(ctx, path, domain)
		if err != nil {
			fmt.Printf("[indexer] Failed to process %s: %v\n", entry.Name(), err)
			continue
		}

		allChunks = append(allChunks, chunks...)
	}

	if len(allChunks) == 0 {
		return nil
	}

	// Generate embeddings in batches
	batchSize := 50
	for i := 0; i < len(allChunks); i += batchSize {
		end := i + batchSize
		if end > len(allChunks) {
			end = len(allChunks)
		}
		batch := allChunks[i:end]

		texts := make([]string, len(batch))
		for j, chunk := range batch {
			texts[j] = chunk.Content
		}

		embeddings, err := idx.embedder.EmbedBatch(ctx, texts)
		if err != nil {
			return fmt.Errorf("failed to generate embeddings: %w", err)
		}

		for j := range batch {
			batch[j].Embedding = embeddings[j]
		}

		if err := idx.store.StoreBatch(ctx, batch); err != nil {
			return fmt.Errorf("failed to store chunks: %w", err)
		}
	}

	return nil
}

// processExpertPrompt extracts chunks from an expert prompt file.
func (idx *Indexer) processExpertPrompt(ctx context.Context, path, domain string) ([]Chunk, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var chunks []Chunk
	now := time.Now()

	// Extract code blocks
	codeBlocks := extractCodeBlocks(string(content))
	for i, block := range codeBlocks {
		chunk := Chunk{
			ID:        GenerateChunkID(path, block.content),
			Source:    fmt.Sprintf("expert:%s", domain),
			Content:   block.content,
			CreatedAt: now,
			Metadata: Metadata{
				ChunkType:  "code",
				Domain:     domain,
				Language:   block.language,
				Tags:       []string{"expert", "pattern", domain},
				TokenCount: estimateTokens(block.content),
			},
		}
		chunks = append(chunks, chunk)

		// Also extract the context (text before the code block)
		if block.context != "" && len(block.context) > 50 {
			contextChunk := Chunk{
				ID:        GenerateChunkID(path, block.context+fmt.Sprintf("-%d", i)),
				Source:    fmt.Sprintf("expert:%s", domain),
				Content:   block.context,
				CreatedAt: now,
				Metadata: Metadata{
					ChunkType:  "pattern",
					Domain:     domain,
					Tags:       []string{"expert", "context", domain},
					TokenCount: estimateTokens(block.context),
				},
			}
			chunks = append(chunks, contextChunk)
		}
	}

	// Extract sections (## headers and their content)
	sections := extractSections(string(content))
	for _, section := range sections {
		if section.tokenCount < 50 || section.tokenCount > 2000 {
			continue // Skip very small or very large sections
		}

		chunk := Chunk{
			ID:        GenerateChunkID(path, section.title),
			Source:    fmt.Sprintf("expert:%s", domain),
			Content:   fmt.Sprintf("## %s\n\n%s", section.title, section.content),
			CreatedAt: now,
			Metadata: Metadata{
				ChunkType:  "expert",
				Domain:     domain,
				Tags:       []string{"expert", "section", domain, strings.ToLower(section.title)},
				TokenCount: section.tokenCount,
			},
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// codeBlock represents an extracted code block.
type codeBlock struct {
	language string
	content  string
	context  string // text before the block
}

// extractCodeBlocks extracts code blocks from markdown.
func extractCodeBlocks(content string) []codeBlock {
	var blocks []codeBlock

	// Match ```language\ncode\n```
	re := regexp.MustCompile("(?s)```(\\w+)?\\n(.*?)\\n```")
	matches := re.FindAllStringSubmatchIndex(content, -1)

	prevEnd := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		langStart, langEnd := match[2], match[3]
		codeStart, codeEnd := match[4], match[5]

		language := ""
		if langStart >= 0 && langEnd >= 0 {
			language = content[langStart:langEnd]
		}

		code := content[codeStart:codeEnd]

		// Get context (text before this block, up to 500 chars)
		contextStart := prevEnd
		if start-prevEnd > 500 {
			contextStart = start - 500
		}
		context := strings.TrimSpace(content[contextStart:start])

		blocks = append(blocks, codeBlock{
			language: language,
			content:  code,
			context:  context,
		})

		prevEnd = end
	}

	return blocks
}

// section represents a markdown section.
type section struct {
	title      string
	content    string
	tokenCount int
}

// extractSections extracts ## sections from markdown.
func extractSections(content string) []section {
	var sections []section

	// Split by ## headers
	re := regexp.MustCompile(`(?m)^## (.+)$`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for i, match := range matches {
		titleStart, titleEnd := match[2], match[3]
		title := content[titleStart:titleEnd]

		// Content runs until next ## or end
		contentStart := match[1]
		contentEnd := len(content)
		if i+1 < len(matches) {
			contentEnd = matches[i+1][0]
		}

		sectionContent := strings.TrimSpace(content[contentStart:contentEnd])

		sections = append(sections, section{
			title:      title,
			content:    sectionContent,
			tokenCount: estimateTokens(sectionContent),
		})
	}

	return sections
}

// IndexConversation indexes a PRD conversation for later retrieval.
func (idx *Indexer) IndexConversation(ctx context.Context, ticketID string, conversation interface{}) error {
	// TODO: Implement conversation indexing
	// This would store expert inputs and PM prompts for retrieval
	// in subsequent rounds or similar future tickets
	return nil
}

// IndexCodePatterns indexes code patterns from the codebase.
func (idx *Indexer) IndexCodePatterns(ctx context.Context, repoPath string, patterns []string) error {
	var allChunks []Chunk
	now := time.Now()

	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(repoPath, pattern))
		if err != nil {
			continue
		}

		for _, match := range matches {
			content, err := os.ReadFile(match)
			if err != nil {
				continue
			}

			// Determine language from extension
			ext := filepath.Ext(match)
			language := extensionToLanguage(ext)

			// Chunk the file
			chunks := ChunkText(string(content), 500)
			for i, chunk := range chunks {
				if len(chunk) < 50 {
					continue
				}

				allChunks = append(allChunks, Chunk{
					ID:        GenerateChunkID(match, fmt.Sprintf("%d", i)),
					Source:    match,
					Content:   chunk,
					CreatedAt: now,
					Metadata: Metadata{
						ChunkType:  "code",
						Language:   language,
						Tags:       []string{"codebase", language},
						TokenCount: estimateTokens(chunk),
					},
				})
			}
		}
	}

	if len(allChunks) == 0 {
		return nil
	}

	// Generate embeddings and store
	batchSize := 50
	for i := 0; i < len(allChunks); i += batchSize {
		end := i + batchSize
		if end > len(allChunks) {
			end = len(allChunks)
		}
		batch := allChunks[i:end]

		texts := make([]string, len(batch))
		for j, chunk := range batch {
			texts[j] = chunk.Content
		}

		embeddings, err := idx.embedder.EmbedBatch(ctx, texts)
		if err != nil {
			return err
		}

		for j := range batch {
			batch[j].Embedding = embeddings[j]
		}

		if err := idx.store.StoreBatch(ctx, batch); err != nil {
			return err
		}
	}

	return nil
}

// extensionToLanguage maps file extensions to language names.
func extensionToLanguage(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".cs":
		return "csharp"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".kt":
		return "kotlin"
	case ".rb":
		return "ruby"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	default:
		return ""
	}
}
