package agents

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/madhatter5501/Factory/agents/anthropic"
	"github.com/madhatter5501/Factory/agents/rag"
)

// TokenUsage is an alias for anthropic.TokenUsage for external use.
type TokenUsage = anthropic.TokenUsage

// SpawnerMode defines the mode of agent spawning.
type SpawnerMode string

const (
	// SpawnerModeCLI uses the Claude CLI for agent execution.
	SpawnerModeCLI SpawnerMode = "cli"

	// SpawnerModeAPI uses the Anthropic API directly with prompt caching.
	SpawnerModeAPI SpawnerMode = "api"

	// SpawnerModeAuto automatically selects based on available credentials.
	SpawnerModeAuto SpawnerMode = "auto"
)

// AgentSpawner is the interface for spawning agents.
type AgentSpawner interface {
	// SpawnAgent runs an agent with the given configuration.
	SpawnAgent(ctx context.Context, agentType AgentType, data PromptData, workDir string) (*AgentResult, error)

	// ValidateAgentEnvironment checks that the spawner is properly configured.
	ValidateAgentEnvironment() []string
}

// SpawnerFactory creates spawners based on configuration.
type SpawnerFactory struct {
	config SpawnerConfig
}

// SpawnerConfig configures the spawner factory.
type SpawnerConfig struct {
	Mode       SpawnerMode   `json:"mode"`
	PromptsDir string        `json:"prompts_dir"`
	Timeout    time.Duration `json:"timeout"`
	Verbose    bool          `json:"verbose"`
	Model      string        `json:"model,omitempty"`

	// API mode settings
	RAGEnabled   bool   `json:"rag_enabled"`
	VectorDBPath string `json:"vector_db_path,omitempty"`

	// Indexing settings
	IndexOnStartup bool     `json:"index_on_startup"`
	IndexPatterns  []string `json:"index_patterns,omitempty"`
}

// DefaultSpawnerConfig returns a default configuration.
func DefaultSpawnerConfig(promptsDir string) SpawnerConfig {
	return SpawnerConfig{
		Mode:           SpawnerModeAuto,
		PromptsDir:     promptsDir,
		Timeout:        30 * time.Minute,
		Verbose:        true,
		RAGEnabled:     true,
		VectorDBPath:   "rag.db",
		IndexOnStartup: true,
		IndexPatterns: []string{
			"packages/dotnet/**/*.cs",
			"packages/web/**/*.ts",
			"agents/**/*.go",
		},
	}
}

// NewSpawnerFactory creates a new spawner factory.
func NewSpawnerFactory(config SpawnerConfig) *SpawnerFactory {
	return &SpawnerFactory{config: config}
}

// CreateSpawner creates a spawner based on the configuration.
func (f *SpawnerFactory) CreateSpawner() (AgentSpawner, error) {
	mode := f.resolveMode()

	switch mode {
	case SpawnerModeAPI:
		return f.createAPISpawner()
	case SpawnerModeCLI:
		return f.createCLISpawner()
	default:
		return nil, fmt.Errorf("unknown spawner mode: %s", mode)
	}
}

// resolveMode determines the actual mode to use.
func (f *SpawnerFactory) resolveMode() SpawnerMode {
	if f.config.Mode != SpawnerModeAuto {
		return f.config.Mode
	}

	// Auto-detect: prefer API if key is available
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return SpawnerModeAPI
	}

	return SpawnerModeCLI
}

// createAPISpawner creates an API-based spawner.
func (f *SpawnerFactory) createAPISpawner() (*APISpawner, error) {
	cfg := APISpawnerConfig{
		PromptsDir:   f.config.PromptsDir,
		Timeout:      f.config.Timeout,
		Verbose:      f.config.Verbose,
		Model:        f.config.Model,
		RAGEnabled:   f.config.RAGEnabled,
		VectorDBPath: f.config.VectorDBPath,
	}

	spawner, err := NewAPISpawner(cfg)
	if err != nil {
		return nil, err
	}

	// Index expert prompts on startup if enabled
	if f.config.IndexOnStartup && f.config.RAGEnabled {
		if err := f.indexExpertPrompts(spawner); err != nil {
			if f.config.Verbose {
				fmt.Printf("[spawner-factory] Failed to index expert prompts: %v\n", err)
			}
			// Non-fatal - continue without indexing
		}
	}

	return spawner, nil
}

// indexExpertPrompts indexes expert prompts for RAG retrieval.
func (f *SpawnerFactory) indexExpertPrompts(spawner *APISpawner) error {
	if spawner.retriever == nil {
		return nil
	}

	embedder, err := rag.NewEmbedder()
	if err != nil {
		return err
	}

	indexer := rag.NewIndexer(spawner.retriever.store, embedder)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if f.config.Verbose {
		fmt.Println("[spawner-factory] Indexing expert prompts for RAG...")
	}

	if err := indexer.IndexExpertPrompts(ctx, f.config.PromptsDir); err != nil {
		return err
	}

	count, _ := spawner.retriever.store.Count(ctx)
	if f.config.Verbose {
		fmt.Printf("[spawner-factory] Indexed %d chunks\n", count)
	}

	return nil
}

// createCLISpawner creates a CLI-based spawner.
func (f *SpawnerFactory) createCLISpawner() (*Spawner, error) {
	return NewSpawner(f.config.PromptsDir, f.config.Timeout, f.config.Verbose, f.config.Model), nil
}

// GetMode returns the current mode.
func (f *SpawnerFactory) GetMode() SpawnerMode {
	return f.resolveMode()
}

// TokenStats returns token usage statistics (API mode only).
func (f *SpawnerFactory) TokenStats(spawner AgentSpawner) *anthropic.TokenUsage {
	if apiSpawner, ok := spawner.(*APISpawner); ok {
		usage := apiSpawner.GetUsage()
		return &usage
	}
	return nil
}

// PrintUsageReport prints a token usage report (API mode only).
func PrintUsageReport(spawner AgentSpawner) {
	apiSpawner, ok := spawner.(*APISpawner)
	if !ok {
		fmt.Println("Token usage tracking only available in API mode")
		return
	}

	usage := apiSpawner.GetUsage()

	fmt.Println("\n=== Token Usage Report ===")
	fmt.Printf("Total Requests:      %d\n", usage.TotalRequests)
	fmt.Printf("Input Tokens:        %d\n", usage.InputTokens)
	fmt.Printf("Output Tokens:       %d\n", usage.OutputTokens)
	fmt.Printf("Cache Creation:      %d\n", usage.CacheCreationInput)
	fmt.Printf("Cache Reads:         %d\n", usage.CacheReadInput)
	fmt.Printf("Cache Hit Rate:      %.1f%%\n", usage.CacheHitRate*100)
	fmt.Printf("Estimated Savings:   $%.4f\n", usage.EstimatedSavings)
	fmt.Println("===========================")
}
