package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// PromptBuilder constructs prompts optimized for caching.
// It separates static content (cached) from dynamic content (per-request).
type PromptBuilder struct {
	promptsDir string
	funcMap    template.FuncMap

	// Cached prompt segments (loaded once, reused across requests)
	sharedRules   string
	expertKnowledge map[string]string // domain -> knowledge
	outputSchemas map[string]string   // agent type -> schema
}

// NewPromptBuilder creates a prompt builder with cached static content.
func NewPromptBuilder(promptsDir string) (*PromptBuilder, error) {
	pb := &PromptBuilder{
		promptsDir:      promptsDir,
		expertKnowledge: make(map[string]string),
		outputSchemas:   make(map[string]string),
		funcMap: template.FuncMap{
			"title": strings.Title,
			"upper": strings.ToUpper,
			"lower": strings.ToLower,
			"join":  strings.Join,
			"sub":   func(a, b int) int { return a - b },
			"add":   func(a, b int) int { return a + b },
			"mul":   func(a, b int) int { return a * b },
			"div":   func(a, b int) int { return a / b },
		},
	}

	// Load shared rules
	sharedPath := filepath.Join(promptsDir, "shared-rules.md")
	if content, err := os.ReadFile(sharedPath); err == nil {
		pb.sharedRules = string(content)
	}

	// Load expert knowledge
	expertsDir := filepath.Join(promptsDir, "experts")
	if entries, err := os.ReadDir(expertsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
				domain := strings.TrimSuffix(entry.Name(), ".md")
				content, _ := os.ReadFile(filepath.Join(expertsDir, entry.Name()))
				pb.expertKnowledge[domain] = string(content)
			}
		}
	}

	return pb, nil
}

// CachedPromptParts represents the two parts of a prompt for caching.
type CachedPromptParts struct {
	// StaticPrefix contains content that should be cached (shared rules, expert knowledge)
	StaticPrefix []SystemBlock

	// DynamicSuffix contains per-request content (ticket, conversation)
	DynamicSuffix []SystemBlock
}

// AgentPromptData contains data for rendering agent prompts.
// This must stay in sync with agents.PromptData for consistent template rendering.
type AgentPromptData struct {
	// Core fields
	Ticket       interface{} `json:"ticket"`
	TicketJSON   string      `json:"ticketJson"`
	WorktreePath string      `json:"worktreePath"`
	BoardStats   interface{} `json:"boardStats"`
	Iteration    interface{} `json:"iteration"`
	AgentName    string      `json:"agentName"`

	// For PM agent
	AllTickets []interface{} `json:"allTickets,omitempty"`

	// Expert consultation
	Domain           string   `json:"domain,omitempty"`
	Questions        []string `json:"questions,omitempty"`
	ConsultationJSON string   `json:"consultationJson,omitempty"`
	ExtraContext     string   `json:"extraContext,omitempty"`

	// PRD collaboration
	Conversation        interface{} `json:"conversation,omitempty"`
	CurrentRound        int         `json:"currentRound,omitempty"`
	CurrentPrompt       string      `json:"currentPrompt,omitempty"`
	Agent               string      `json:"agent,omitempty"`
	FocusAreas          []string    `json:"focusAreas,omitempty"`
	ConversationSummary string      `json:"conversationSummary,omitempty"`
	PRD                 string      `json:"prd,omitempty"`
	FinalExpertInputs   interface{} `json:"finalExpertInputs,omitempty"`

	// RAG-retrieved context (formatted as markdown strings for template usage)
	RetrievedPatterns string `json:"retrievedPatterns,omitempty"`
	RetrievedHistory  string `json:"retrievedHistory,omitempty"`
}

// RetrievedChunk represents a RAG-retrieved content chunk.
type RetrievedChunk struct {
	Source     string  `json:"source"`
	Content    string  `json:"content"`
	Similarity float64 `json:"similarity"`
}

// BuildCachedPrompt constructs a prompt with separate cached and dynamic parts.
func (pb *PromptBuilder) BuildCachedPrompt(agentType string, data AgentPromptData) (*CachedPromptParts, error) {
	parts := &CachedPromptParts{
		StaticPrefix:  make([]SystemBlock, 0),
		DynamicSuffix: make([]SystemBlock, 0),
	}

	// Load agent-specific template
	promptFile := filepath.Join(pb.promptsDir, agentType+".md")
	templateBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read prompt template %s: %w", promptFile, err)
	}

	// Parse the template to extract static vs dynamic sections
	templateContent := string(templateBytes)

	// Split template into cacheable and dynamic sections
	staticPart, dynamicPart := pb.splitTemplate(templateContent, agentType)

	// Render static part (can use minimal data since it shouldn't have dynamic refs)
	staticRendered, err := pb.renderTemplate(staticPart, data)
	if err != nil {
		return nil, fmt.Errorf("failed to render static template: %w", err)
	}

	// Add static prefix with cache control
	if staticRendered != "" {
		parts.StaticPrefix = append(parts.StaticPrefix, SystemBlock{
			Type:         "text",
			Text:         staticRendered,
			CacheControl: Ephemeral(),
		})
	}

	// Add shared rules as cached block
	if pb.sharedRules != "" && strings.Contains(templateContent, "shared-rules") {
		sharedRendered, err := pb.renderTemplate(pb.sharedRules, data)
		if err == nil && sharedRendered != "" {
			parts.StaticPrefix = append(parts.StaticPrefix, SystemBlock{
				Type:         "text",
				Text:         sharedRendered,
				CacheControl: Ephemeral(),
			})
		}
	}

	// Add domain expert knowledge if relevant
	if data.Domain != "" {
		if knowledge, ok := pb.expertKnowledge[data.Domain]; ok {
			// For RAG mode, only include retrieved patterns instead of full knowledge
			if data.RetrievedPatterns != "" {
				parts.StaticPrefix = append(parts.StaticPrefix, SystemBlock{
					Type:         "text",
					Text:         fmt.Sprintf("## Relevant Patterns (Retrieved)\n\n%s", data.RetrievedPatterns),
					CacheControl: Ephemeral(),
				})
			} else {
				// Fallback to full knowledge if no RAG
				parts.StaticPrefix = append(parts.StaticPrefix, SystemBlock{
					Type:         "text",
					Text:         knowledge,
					CacheControl: Ephemeral(),
				})
			}
		}
	}

	// Render dynamic part with full data
	dynamicRendered, err := pb.renderTemplate(dynamicPart, data)
	if err != nil {
		return nil, fmt.Errorf("failed to render dynamic template: %w", err)
	}

	// Add dynamic suffix (no cache control)
	if dynamicRendered != "" {
		parts.DynamicSuffix = append(parts.DynamicSuffix, SystemBlock{
			Type: "text",
			Text: dynamicRendered,
		})
	}

	// Add conversation context (dynamic, changes per round)
	if data.ConversationSummary != "" {
		parts.DynamicSuffix = append(parts.DynamicSuffix, SystemBlock{
			Type: "text",
			Text: fmt.Sprintf("## Conversation Summary\n\n%s", data.ConversationSummary),
		})
	}

	// Add ticket context (dynamic)
	if data.TicketJSON != "" {
		parts.DynamicSuffix = append(parts.DynamicSuffix, SystemBlock{
			Type: "text",
			Text: fmt.Sprintf("## Current Ticket\n\n```json\n%s\n```", data.TicketJSON),
		})
	}

	return parts, nil
}

// splitTemplate separates a template into static (cacheable) and dynamic parts.
func (pb *PromptBuilder) splitTemplate(content, agentType string) (static, dynamic string) {
	// Markers for splitting - anything after these markers is dynamic
	dynamicMarkers := []string{
		"## Ticket Context",
		"## Current Ticket",
		"## Conversation History",
		"## Full Conversation History",
		"{{.TicketJSON}}",
		"{{.Conversation",
	}

	// Find the earliest dynamic marker
	splitPoint := len(content)
	for _, marker := range dynamicMarkers {
		if idx := strings.Index(content, marker); idx != -1 && idx < splitPoint {
			splitPoint = idx
		}
	}

	// Also check for template include directive
	includeMarker := `{{template "shared-rules.md" .}}`
	content = strings.Replace(content, includeMarker, "", 1) // Remove include, we handle separately

	if splitPoint < len(content) {
		return content[:splitPoint], content[splitPoint:]
	}

	// If no dynamic markers, check for percentage-based split
	// First 60% is likely static instructions, rest is dynamic
	staticEnd := len(content) * 60 / 100
	// Find a natural break point (blank line) near 60%
	for i := staticEnd; i < len(content) && i < staticEnd+200; i++ {
		if i+1 < len(content) && content[i] == '\n' && content[i+1] == '\n' {
			return content[:i+2], content[i+2:]
		}
	}

	return content, ""
}

// renderTemplate executes a template with the given data.
func (pb *PromptBuilder) renderTemplate(templateStr string, data AgentPromptData) (string, error) {
	if templateStr == "" {
		return "", nil
	}

	tmpl, err := template.New("prompt").Funcs(pb.funcMap).Parse(templateStr)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// BuildSystemBlocks combines static and dynamic parts into system blocks.
func (pb *PromptBuilder) BuildSystemBlocks(parts *CachedPromptParts) []SystemBlock {
	result := make([]SystemBlock, 0, len(parts.StaticPrefix)+len(parts.DynamicSuffix))
	result = append(result, parts.StaticPrefix...)
	result = append(result, parts.DynamicSuffix...)
	return result
}

// MinimalTicketJSON creates a minimal JSON representation of a ticket.
// This reduces token usage by omitting fields not needed for the current agent.
func MinimalTicketJSON(ticket interface{}, agentType string) (string, error) {
	// Marshal full ticket
	full, err := json.Marshal(ticket)
	if err != nil {
		return "", err
	}

	// Parse into map
	var ticketMap map[string]interface{}
	if err := json.Unmarshal(full, &ticketMap); err != nil {
		return "", err
	}

	// Fields always needed
	keepFields := []string{
		"id", "title", "status", "domain", "priority",
		"acceptance_criteria", "constraints",
	}

	// Agent-specific fields
	switch agentType {
	case "dev-backend", "dev-frontend", "dev-infra":
		keepFields = append(keepFields, "technical_context", "prd")
	case "qa":
		keepFields = append(keepFields, "technical_context", "acceptance_criteria", "test_plan")
	case "ux":
		keepFields = append(keepFields, "prd", "acceptance_criteria", "ux_notes")
	case "security":
		keepFields = append(keepFields, "technical_context", "constraints", "security_review")
	case "pm", "pm-facilitator", "pm-breakdown":
		// PM agents need more context
		keepFields = append(keepFields, "prd", "description", "epic_id", "sub_tickets", "dependencies")
	}

	// Filter to only keep needed fields
	minimal := make(map[string]interface{})
	for _, field := range keepFields {
		if val, ok := ticketMap[field]; ok {
			minimal[field] = val
		}
	}

	minimalJSON, err := json.MarshalIndent(minimal, "", "  ")
	if err != nil {
		return "", err
	}

	return string(minimalJSON), nil
}
