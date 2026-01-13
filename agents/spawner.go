// Package agents provides agent spawning and management for the AI development factory.
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"factory/agents/anthropic"
	"factory/kanban"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// AgentType represents the type of agent.
type AgentType string

const (
	AgentTypePM             AgentType = "pm"
	AgentTypePMRequirements AgentType = "pm-requirements"
	AgentTypeExpertConsult  AgentType = "expert-consultation"
	AgentTypeDevFrontend    AgentType = "dev-frontend"
	AgentTypeDevBackend     AgentType = "dev-backend"
	AgentTypeDevInfra       AgentType = "dev-infra"
	AgentTypeQA             AgentType = "qa"
	AgentTypeUX             AgentType = "ux"
	AgentTypeSecurity       AgentType = "security"
	AgentTypeIdeas          AgentType = "ideas"

	// Collaborative PRD agents.
	AgentTypePMFacilitator AgentType = "pm-facilitator" // PM facilitating multi-round PRD discussion.
	AgentTypePRDExpert     AgentType = "prd-expert"     // Domain expert in PRD discussion
	AgentTypePMBreakdown   AgentType = "pm-breakdown"   // PM breaking PRD into sub-tickets
)

// GetModelForAgent returns the appropriate model for an agent type.
// This implements tiered model selection to optimize costs while maintaining quality.
//
// Tier strategy:
//   - Ideas/triage: Haiku (cheapest, sufficient for simple classification)
//   - All other agents: Sonnet 4 (best quality/cost balance)
//
// The defaultModel parameter is used as a fallback and for most agent types.
func GetModelForAgent(agentType AgentType, defaultModel string) string {
	switch agentType {
	case AgentTypeIdeas:
		// Ideas generation/triage can use cheaper model
		return anthropic.ModelHaiku35
	default:
		// All other agents use the default (Sonnet 4)
		if defaultModel != "" {
			return defaultModel
		}
		return anthropic.ModelSonnet4
	}
}

// AgentResult represents the outcome of an agent run.
type AgentResult struct {
	Success   bool          `json:"success"`
	TicketID  string        `json:"ticketId,omitempty"`
	AgentType AgentType     `json:"agentType"`
	Output    string        `json:"output"`
	Duration  time.Duration `json:"duration"`
	Error     string        `json:"error,omitempty"`
	ExitCode  int           `json:"exitCode"`
}

// Spawner manages spawning and running AI agents.
type Spawner struct {
	promptsDir   string        // Directory containing prompt templates
	claudePath   string        // Path to claude CLI
	timeout      time.Duration // Timeout for agent runs
	verbose      bool          // Print agent output
	defaultModel string        // Default model (empty uses CLI default)
}

// NewSpawner creates a new agent spawner.
// If model is empty, uses GetModelForAgent() to select appropriate model per agent type.
func NewSpawner(promptsDir string, timeout time.Duration, verbose bool, model string) *Spawner {
	claudePath := "claude"
	// Try to find claude in common locations
	if path, err := exec.LookPath("claude"); err == nil {
		claudePath = path
	}

	// Default to Sonnet 4 if no model specified (prevents expensive Opus usage)
	if model == "" {
		model = anthropic.ModelSonnet4
	}

	return &Spawner{
		promptsDir:   promptsDir,
		claudePath:   claudePath,
		timeout:      timeout,
		verbose:      verbose,
		defaultModel: model,
	}
}

// PromptData contains data passed to prompt templates.
type PromptData struct {
	Ticket       *kanban.Ticket        `json:"ticket"`
	TicketJSON   string                `json:"ticketJson"`
	WorktreePath string                `json:"worktreePath"`
	BoardStats   map[kanban.Status]int `json:"boardStats"`
	Iteration    *kanban.Iteration     `json:"iteration"`

	// Agent identification (used in shared-rules.md for logging)
	AgentName string `json:"agentName"`

	// For PM agent
	AllTickets []kanban.Ticket `json:"allTickets,omitempty"`
	RawIdea    string          `json:"rawIdea,omitempty"` // User-submitted idea for backlog processing

	// For expert consultation
	Questions        []string `json:"questions,omitempty"`
	ConsultationJSON string   `json:"consultationJson,omitempty"` // Serialized consultation request

	// Agent-specific config
	Domain       string `json:"domain,omitempty"`
	ExtraContext string `json:"extraContext,omitempty"`

	// For collaborative PRD discussion
	Conversation        *kanban.PRDConversation       `json:"conversation,omitempty"`
	CurrentRound        int                           `json:"currentRound,omitempty"`
	CurrentPrompt       string                        `json:"currentPrompt,omitempty"`
	Agent               string                        `json:"agent,omitempty"`               // dev, qa, ux, security
	FocusAreas          []string                      `json:"focusAreas,omitempty"`          // Specific questions for this agent
	ConversationSummary string                        `json:"conversationSummary,omitempty"` // Summary for breakdown
	PRD                 string                        `json:"prd,omitempty"`                 // Final PRD for breakdown
	FinalExpertInputs   map[string]kanban.ExpertInput `json:"finalExpertInputs,omitempty"`   // Last round's expert inputs

	// RAG-retrieved context (API mode only)
	RetrievedPatterns string `json:"retrievedPatterns,omitempty"` // Relevant code patterns
	RetrievedHistory  string `json:"retrievedHistory,omitempty"`  // Relevant conversation history
}

// SpawnAgent runs an agent with the given configuration.
func (s *Spawner) SpawnAgent(ctx context.Context, agentType AgentType, data PromptData, workDir string) (*AgentResult, error) {
	startTime := time.Now()

	// Load and render prompt template
	prompt, err := s.renderPrompt(agentType, data)
	if err != nil {
		return &AgentResult{
			Success:   false,
			AgentType: agentType,
			Error:     fmt.Sprintf("failed to render prompt: %v", err),
		}, err
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Get appropriate model for this agent type
	model := GetModelForAgent(agentType, s.defaultModel)

	// Run claude CLI with model selection
	result, err := s.runClaude(ctx, prompt, workDir, model)
	result.AgentType = agentType
	result.Duration = time.Since(startTime)

	if data.Ticket != nil {
		result.TicketID = data.Ticket.ID
	}

	return result, err
}

// runClaude executes the claude CLI with the given prompt.
func (s *Spawner) runClaude(ctx context.Context, prompt string, workDir string, model string) (*AgentResult, error) {
	// Build command args
	args := []string{
		"--print",                        // Print output instead of interactive
		"--dangerously-skip-permissions", // Skip permission prompts for automation
	}

	// Add model flag if specified (prevents inheriting expensive CLI defaults like Opus)
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, s.claudePath, args...) // #nosec G204 -- claudePath is validated at construction time

	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	if s.verbose {
		// Write to both buffer AND console for verbose mode
		cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	err := cmd.Run()

	result := &AgentResult{
		Success:  err == nil,
		Output:   stdout.String(),
		ExitCode: 0,
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Error = stderr.String()
	}

	// Check for completion signal in output
	if strings.Contains(result.Output, "<promise>") {
		result.Success = true
	}

	return result, err
}

// templateFuncs provides custom functions for prompt templates.
var templateFuncs = template.FuncMap{
	"title": cases.Title(language.English).String, // Title cases a string (e.g., "dev" -> "Dev").
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"join":  strings.Join,
	// Math functions.
	"sub": func(a, b int) int { return a - b },
	"add": func(a, b int) int { return a + b },
	"mul": func(a, b int) int { return a * b },
	"div": func(a, b int) int { return a / b },
}

// renderPrompt loads a prompt template and renders it with the given data.
func (s *Spawner) renderPrompt(agentType AgentType, data PromptData) (string, error) {
	// Set agent name for logging in shared-rules.md
	data.AgentName = string(agentType)

	// Determine prompt file
	promptFile := filepath.Join(s.promptsDir, string(agentType)+".md")

	// Read main template
	templateBytes, err := os.ReadFile(promptFile) // #nosec G304 -- promptsDir from internal config
	if err != nil {
		return "", fmt.Errorf("failed to read prompt template %s: %w", promptFile, err)
	}

	// Serialize ticket to JSON for embedding
	if data.Ticket != nil {
		ticketJSON, _ := json.MarshalIndent(data.Ticket, "", "  ")
		data.TicketJSON = string(ticketJSON)
	}

	// Create template with custom functions
	tmpl := template.New("prompt").Funcs(templateFuncs)

	// Load shared-rules.md as a named template for {{template "shared-rules.md" .}}
	sharedRulesPath := filepath.Join(s.promptsDir, "shared-rules.md")
	// #nosec G304 -- promptsDir is from internal config, not user input
	if sharedRulesBytes, err := os.ReadFile(sharedRulesPath); err == nil {
		_, err = tmpl.New("shared-rules.md").Parse(string(sharedRulesBytes))
		if err != nil {
			return "", fmt.Errorf("failed to parse shared-rules template: %w", err)
		}
	}

	// Load expert templates for expert consultation
	expertsDir := filepath.Join(s.promptsDir, "experts")
	if entries, err := os.ReadDir(expertsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
				expertPath := filepath.Join(expertsDir, entry.Name())
				// #nosec G304 -- path from internal config, not user input
				if expertBytes, err := os.ReadFile(expertPath); err == nil {
					_, _ = tmpl.New(entry.Name()).Parse(string(expertBytes))
				}
			}
		}
	}

	// Parse the main template
	_, err = tmpl.Parse(string(templateBytes))
	if err != nil {
		return "", fmt.Errorf("failed to parse prompt template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render prompt: %w", err)
	}

	return buf.String(), nil
}

// GetAgentTypeForDomain returns the appropriate dev agent type for a domain.
func GetAgentTypeForDomain(domain kanban.Domain) AgentType {
	switch domain {
	case kanban.DomainFrontend:
		return AgentTypeDevFrontend
	case kanban.DomainBackend:
		return AgentTypeDevBackend
	case kanban.DomainInfra:
		return AgentTypeDevInfra
	default:
		return AgentTypeDevBackend // Default to backend
	}
}

// GetNextStageAgent returns the agent type for the next pipeline stage.
func GetNextStageAgent(currentStatus kanban.Status) (AgentType, kanban.Status) {
	switch currentStatus {
	case kanban.StatusInDev:
		return AgentTypeQA, kanban.StatusInQA
	case kanban.StatusInQA:
		return AgentTypeUX, kanban.StatusInUX
	case kanban.StatusInUX:
		return AgentTypeSecurity, kanban.StatusInSec
	case kanban.StatusInSec:
		return AgentTypePM, kanban.StatusPMReview
	default:
		return "", ""
	}
}

// ValidateAgentEnvironment checks that the agent environment is properly configured.
func (s *Spawner) ValidateAgentEnvironment() []string {
	var errors []string

	// Check claude CLI
	if _, err := exec.LookPath(s.claudePath); err != nil {
		errors = append(errors, fmt.Sprintf("claude CLI not found at %s", s.claudePath))
	}

	// Check prompts directory
	if _, err := os.Stat(s.promptsDir); os.IsNotExist(err) {
		errors = append(errors, fmt.Sprintf("prompts directory not found: %s", s.promptsDir))
	}

	// Check for required prompt files
	requiredPrompts := []AgentType{
		AgentTypePM,
		AgentTypeDevFrontend,
		AgentTypeDevBackend,
		AgentTypeQA,
		AgentTypeUX,
		AgentTypeSecurity,
	}

	for _, agent := range requiredPrompts {
		promptFile := filepath.Join(s.promptsDir, string(agent)+".md")
		if _, err := os.Stat(promptFile); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("missing prompt file: %s", promptFile))
		}
	}

	return errors
}
