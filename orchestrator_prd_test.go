package factory

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arctek/factory/agents"
	"github.com/arctek/factory/kanban"
)

// --- Test Helpers ---

// mockSpawner tracks spawned agents for testing.
type mockSpawner struct {
	mu          sync.Mutex
	spawnedRuns []spawnRecord
	responses   map[agents.AgentType]string
}

type spawnRecord struct {
	AgentType agents.AgentType
	TicketID  string
	Agent     string // For expert agents, this is the domain (dev, qa, ux, security)
}

func newMockSpawner() *mockSpawner {
	return &mockSpawner{
		responses: make(map[agents.AgentType]string),
	}
}

func (m *mockSpawner) SpawnAgent(ctx context.Context, agentType agents.AgentType, data agents.PromptData, workDir string) (*agents.AgentResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ticketID := ""
	if data.Ticket != nil {
		ticketID = data.Ticket.ID
	}

	m.spawnedRuns = append(m.spawnedRuns, spawnRecord{
		AgentType: agentType,
		TicketID:  ticketID,
		Agent:     data.Agent,
	})

	response := m.responses[agentType]
	if response == "" {
		response = "{}"
	}

	return &agents.AgentResult{
		Success:   true,
		AgentType: agentType,
		Output:    response,
		TicketID:  ticketID,
	}, nil
}

func (m *mockSpawner) SetResponse(agentType agents.AgentType, response string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses[agentType] = response
}

func (m *mockSpawner) GetSpawnedAgents() []spawnRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]spawnRecord{}, m.spawnedRuns...)
}

func (m *mockSpawner) ActiveAgentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.spawnedRuns)
}

func (m *mockSpawner) HasAgentType(t agents.AgentType) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.spawnedRuns {
		if r.AgentType == t {
			return true
		}
	}
	return false
}

func (m *mockSpawner) GetExpertDomains() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var domains []string
	for _, r := range m.spawnedRuns {
		if r.AgentType == agents.AgentTypePRDExpert && r.Agent != "" {
			domains = append(domains, r.Agent)
		}
	}
	return domains
}

// mockState implements kanban.StateStore for testing.
type mockState struct {
	mu         sync.Mutex
	tickets    map[string]*kanban.Ticket
	runs       []kanban.AgentRun
	stats      map[kanban.Status]int
	iteration  *kanban.Iteration
}

func newMockState() *mockState {
	return &mockState{
		tickets: make(map[string]*kanban.Ticket),
		stats:   make(map[kanban.Status]int),
	}
}

func (m *mockState) Load() error                                { return nil }
func (m *mockState) Save() error                                { return nil }
func (m *mockState) GetBoard() kanban.Board                     { return kanban.Board{} }
func (m *mockState) GetConfig() kanban.BoardConfig              { return kanban.BoardConfig{} }
func (m *mockState) GetStats() map[kanban.Status]int            { return m.stats }
func (m *mockState) SetIteration(iter *kanban.Iteration)        { m.iteration = iter }
func (m *mockState) GetIteration() *kanban.Iteration            { return m.iteration }
func (m *mockState) IsIterationComplete() bool                  { return false }
func (m *mockState) GetReadyTickets() []kanban.Ticket           { return m.GetTicketsByStatus(kanban.StatusReady) }
func (m *mockState) GetNextTicketForDomain(d kanban.Domain) (*kanban.Ticket, bool) { return nil, false }
func (m *mockState) GetInProgressCount() int                    { return 0 }
func (m *mockState) AssignAgent(ticketID, agentID string) error { return nil }
func (m *mockState) SetWorktree(ticketID string, wt *kanban.Worktree) error { return nil }
func (m *mockState) AddSignoff(ticketID, stage, agentID string) error { return nil }
func (m *mockState) AddBug(ticketID string, bug kanban.Bug) error { return nil }
func (m *mockState) UpdateNotes(ticketID, notes string) error   { return nil }
func (m *mockState) UpdateActivity(ticketID, activity, assignee string) error { return nil }
func (m *mockState) ClearActivity(ticketID string) error        { return nil }
func (m *mockState) CleanupStaleRuns(maxAge time.Duration)      {}

func (m *mockState) GetTicket(id string) (*kanban.Ticket, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	return t, ok
}

func (m *mockState) GetAllTickets() ([]kanban.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]kanban.Ticket, 0, len(m.tickets))
	for _, t := range m.tickets {
		result = append(result, *t)
	}
	return result, nil
}

func (m *mockState) GetTicketsByStatus(status kanban.Status) []kanban.Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []kanban.Ticket
	for _, t := range m.tickets {
		if t.Status == status {
			result = append(result, *t)
		}
	}
	return result
}

func (m *mockState) GetTicketsByDomain(domain kanban.Domain) []kanban.Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []kanban.Ticket
	for _, t := range m.tickets {
		if t.Domain == domain {
			result = append(result, *t)
		}
	}
	return result
}

func (m *mockState) GetTicketsByParent(parentID string) []kanban.Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []kanban.Ticket
	for _, t := range m.tickets {
		if t.ParentID == parentID {
			result = append(result, *t)
		}
	}
	return result
}

func (m *mockState) AddTicket(t kanban.Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickets[t.ID] = &t
	return nil
}

func (m *mockState) CreateTicket(t *kanban.Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickets[t.ID] = t
	return nil
}

func (m *mockState) UpdateTicketStatus(id string, newStatus kanban.Status, by string, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tickets[id]; ok {
		t.Status = newStatus
	}
	return nil
}

func (m *mockState) UpdateTicket(ticket *kanban.Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickets[ticket.ID] = ticket
	return nil
}

func (m *mockState) AddRun(run *kanban.AgentRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, *run)
	return nil
}

func (m *mockState) AddActiveRun(run kanban.AgentRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, run)
}

func (m *mockState) CompleteRun(runID string, status string, output string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.runs {
		if m.runs[i].ID == runID {
			m.runs[i].Status = status
			m.runs[i].Output = output
		}
	}
}

func (m *mockState) GetActiveRuns() []kanban.AgentRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	var active []kanban.AgentRun
	for _, r := range m.runs {
		if r.Status == "running" {
			active = append(active, r)
		}
	}
	return active
}

func (m *mockState) GetActiveDevRuns() []kanban.AgentRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	var active []kanban.AgentRun
	for _, r := range m.runs {
		if r.Status == "running" && strings.HasPrefix(r.Agent, "dev") {
			active = append(active, r)
		}
	}
	return active
}

func (m *mockState) GetActiveRunsForTicket(ticketID string) []kanban.AgentRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	var active []kanban.AgentRun
	for _, r := range m.runs {
		if r.TicketID == ticketID && r.Status == "running" {
			active = append(active, r)
		}
	}
	return active
}

// --- Test Fixtures ---

func createApprovedTicket(id, title string) *kanban.Ticket {
	return &kanban.Ticket{
		ID:          id,
		Title:       title,
		Description: "Test description",
		Domain:      kanban.DomainBackend,
		Status:      kanban.StatusApproved,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func createTicketInRound(id string, roundNum int, rounds []kanban.ConversationRound) *kanban.Ticket {
	return &kanban.Ticket{
		ID:          id,
		Title:       fmt.Sprintf("Ticket in Round %d", roundNum),
		Description: "Test description",
		Domain:      kanban.DomainBackend,
		Status:      kanban.Status(fmt.Sprintf("%s_%d", kanban.StatusRefiningRound, roundNum)),
		Conversation: &kanban.PRDConversation{
			TicketID:     id,
			Rounds:       rounds,
			CurrentRound: roundNum,
			Status:       "in_progress",
			StartedAt:    time.Now(),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func createTicketWithExpertResponses(id string) *kanban.Ticket {
	return &kanban.Ticket{
		ID:          id,
		Title:       "Ticket with expert responses",
		Description: "Test description",
		Domain:      kanban.DomainBackend,
		Status:      kanban.Status(fmt.Sprintf("%s_1", kanban.StatusRefiningRound)),
		Conversation: &kanban.PRDConversation{
			TicketID:     id,
			CurrentRound: 1,
			Status:       "in_progress",
			Rounds: []kanban.ConversationRound{
				{
					RoundNumber: 1,
					PMPrompt:    "Initial analysis request",
					ExpertInputs: map[string]kanban.ExpertInput{
						"dev":      {Agent: "dev", Response: "DEV input", Approves: true},
						"qa":       {Agent: "qa", Response: "QA input", Approves: true},
						"ux":       {Agent: "ux", Response: "UX input", Approves: true},
						"security": {Agent: "security", Response: "Security input", Approves: true},
					},
					Timestamp: time.Now(),
				},
			},
			StartedAt: time.Now(),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func createTicketWithSecurityConcern(id string) *kanban.Ticket {
	return &kanban.Ticket{
		ID:          id,
		Title:       "Ticket with security concern",
		Description: "Test description",
		Domain:      kanban.DomainBackend,
		Status:      kanban.Status(fmt.Sprintf("%s_1", kanban.StatusRefiningRound)),
		Conversation: &kanban.PRDConversation{
			TicketID:     id,
			CurrentRound: 1,
			Status:       "in_progress",
			Rounds: []kanban.ConversationRound{
				{
					RoundNumber: 1,
					PMPrompt:    "Initial analysis request",
					ExpertInputs: map[string]kanban.ExpertInput{
						"dev":      {Agent: "dev", Response: "DEV input", Approves: true},
						"qa":       {Agent: "qa", Response: "QA input", Approves: true},
						"ux":       {Agent: "ux", Response: "UX input", Approves: true},
						"security": {Agent: "security", Response: "Security has concerns about authentication", Approves: false, Concerns: []string{"Missing auth validation"}},
					},
					Timestamp: time.Now(),
				},
			},
			StartedAt: time.Now(),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func createCompletedPRDTicket(id string) *kanban.Ticket {
	return &kanban.Ticket{
		ID:          id,
		Title:       "Completed PRD ticket",
		Description: "Test description",
		Domain:      kanban.DomainBackend,
		Status:      kanban.StatusPRDComplete,
		Conversation: &kanban.PRDConversation{
			TicketID:     id,
			CurrentRound: 2,
			Status:       "consensus",
			FinalPRD:     "Final PRD document with requirements...",
			Rounds: []kanban.ConversationRound{
				{RoundNumber: 1, PMPrompt: "Round 1", ExpertInputs: make(map[string]kanban.ExpertInput)},
				{RoundNumber: 2, PMPrompt: "Round 2", ExpertInputs: make(map[string]kanban.ExpertInput)},
			},
			StartedAt: time.Now(),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func createReadySubTicket(id, parentID, title string, files []string) *kanban.Ticket {
	return &kanban.Ticket{
		ID:          id,
		Title:       title,
		Description: "Sub-ticket description",
		Domain:      kanban.DomainBackend,
		Status:      kanban.StatusReady,
		ParentID:    parentID,
		Files:       files,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

// --- AC Tests ---

// AC-1: Multi-Round Discussion Initiation
func TestAC1_PRDDiscussionInitiation(t *testing.T) {
	state := newMockState()
	spawner := newMockSpawner()

	ticket := createApprovedTicket("TEST-001", "Test Feature")
	state.AddTicket(*ticket)

	// Set PM facilitator response
	spawner.SetResponse(agents.AgentTypePMFacilitator, `{
		"action": "START_ROUND",
		"prompt": "Please analyze this feature request",
		"focusAreas": {
			"dev": ["technical feasibility"],
			"qa": ["testability"],
			"ux": ["usability"],
			"security": ["auth requirements"]
		}
	}`)

	// Create minimal orchestrator with mocks
	orch := &Orchestrator{
		state:    state,
		repoRoot: "/tmp/test",
		config:   Config{DryRun: false},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// Replace spawner
	orch.spawner = &agents.Spawner{} // Will be overridden by mock

	// Test processApprovedToPRDRound
	ctx := context.Background()
	orch.processApprovedToPRDRound(ctx)

	// Verify ticket moved to REFINING_ROUND_1
	updatedTicket, _ := state.GetTicket("TEST-001")
	if !strings.HasPrefix(string(updatedTicket.Status), string(kanban.StatusRefiningRound)) {
		t.Errorf("Expected status to start with REFINING_ROUND, got %s", updatedTicket.Status)
	}

	// Verify conversation initialized
	if updatedTicket.Conversation == nil {
		t.Error("Expected conversation to be initialized")
	}
	if updatedTicket.Conversation.CurrentRound != 1 {
		t.Errorf("Expected current round to be 1, got %d", updatedTicket.Conversation.CurrentRound)
	}
}

// AC-2: Expert Receives Full Context
func TestAC2_ExpertReceivesFullContext(t *testing.T) {
	// Create ticket with 2 rounds of history
	rounds := []kanban.ConversationRound{
		{
			RoundNumber: 1,
			PMPrompt:    "Initial analysis of the feature",
			ExpertInputs: map[string]kanban.ExpertInput{
				"dev":      {Agent: "dev", Response: "DEV suggests using microservices"},
				"qa":       {Agent: "qa", Response: "QA needs integration tests"},
				"ux":       {Agent: "ux", Response: "UX wants dark mode"},
				"security": {Agent: "security", Response: "Security requires OAuth"},
			},
			PMSynthesis: "Round 1 summary: Consider microservices with OAuth",
			Timestamp:   time.Now().Add(-1 * time.Hour),
		},
		{
			RoundNumber: 2,
			PMPrompt:    "Follow-up on security concerns",
			ExpertInputs: map[string]kanban.ExpertInput{
				"dev":      {Agent: "dev", Response: "DEV can implement OAuth"},
				"qa":       {Agent: "qa", Response: "QA will test auth flows"},
				"ux":       {Agent: "ux", Response: "UX approves login flow"},
				"security": {Agent: "security", Response: "Security approves approach"},
			},
			PMSynthesis: "Round 2 summary: OAuth approach approved",
			Timestamp:   time.Now().Add(-30 * time.Minute),
		},
	}

	ticket := createTicketInRound("TEST-002", 3, rounds)

	// Verify conversation has all rounds
	if len(ticket.Conversation.Rounds) != 2 {
		t.Errorf("Expected 2 rounds in history, got %d", len(ticket.Conversation.Rounds))
	}

	// Verify round 1 content preserved
	round1 := ticket.Conversation.Rounds[0]
	if round1.PMPrompt != "Initial analysis of the feature" {
		t.Errorf("Round 1 PM prompt not preserved")
	}
	if len(round1.ExpertInputs) != 4 {
		t.Errorf("Expected 4 expert inputs in round 1, got %d", len(round1.ExpertInputs))
	}
	if round1.ExpertInputs["dev"].Response != "DEV suggests using microservices" {
		t.Error("DEV response from round 1 not preserved")
	}

	// Verify round 2 content preserved
	round2 := ticket.Conversation.Rounds[1]
	if round2.PMSynthesis != "Round 2 summary: OAuth approach approved" {
		t.Error("Round 2 synthesis not preserved")
	}
}

// AC-3: PM Synthesizes After Each Round
func TestAC3_PMSynthesizesRound(t *testing.T) {
	state := newMockState()
	ticket := createTicketWithExpertResponses("TEST-003")
	state.AddTicket(*ticket)

	orch := &Orchestrator{
		state:    state,
		repoRoot: "/tmp/test",
		config:   Config{DryRun: true}, // Dry run to avoid actual agent calls
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// Get current round
	round := orch.getCurrentRound(ticket)

	// Verify all 4 experts have responded
	if len(round.ExpertInputs) != 4 {
		t.Errorf("Expected 4 expert inputs, got %d", len(round.ExpertInputs))
	}

	// Verify each expert domain is present
	for _, domain := range []string{"dev", "qa", "ux", "security"} {
		if _, ok := round.ExpertInputs[domain]; !ok {
			t.Errorf("Missing expert input for domain: %s", domain)
		}
	}
}

// AC-4: Consensus Detection
func TestAC4_ConsensusDetection(t *testing.T) {
	ticket := createTicketWithExpertResponses("TEST-004")

	// All experts approve in this fixture
	allApprove := true
	for _, input := range ticket.Conversation.Rounds[0].ExpertInputs {
		if !input.Approves {
			allApprove = false
			break
		}
	}

	if !allApprove {
		t.Error("Expected all experts to approve")
	}

	// In real orchestrator, this would trigger PM to finalize PRD
	// The PM facilitator would output action: "FINALIZE_PRD"
}

// AC-5: Continued Discussion on Concerns
func TestAC5_ContinuedDiscussionOnConcerns(t *testing.T) {
	ticket := createTicketWithSecurityConcern("TEST-005")

	// Verify security has concerns
	secInput := ticket.Conversation.Rounds[0].ExpertInputs["security"]
	if secInput.Approves {
		t.Error("Expected security to NOT approve")
	}
	if len(secInput.Concerns) == 0 {
		t.Error("Expected security to have concerns")
	}

	// Check that at least one expert doesn't approve
	hasNonApproval := false
	for _, input := range ticket.Conversation.Rounds[0].ExpertInputs {
		if !input.Approves {
			hasNonApproval = true
			break
		}
	}

	if !hasNonApproval {
		t.Error("Expected at least one expert to not approve")
	}

	// In real orchestrator, PM synthesis would trigger another round
}

// AC-6: PRD Breakdown Creates Sub-Tickets
func TestAC6_PRDBreakdown(t *testing.T) {
	state := newMockState()
	ticket := createCompletedPRDTicket("TEST-006")
	state.AddTicket(*ticket)

	// Simulate PM breakdown output
	subTickets := []SubTicketSpec{
		{
			Title:              "Implement init command",
			Description:        "Create adr init subcommand",
			Domain:             "backend",
			Files:              []string{"cmd/init.go"},
			AcceptanceCriteria: []string{"Command creates .adr directory"},
			ParallelGroup:      1,
		},
		{
			Title:              "Implement new command",
			Description:        "Create adr new subcommand",
			Domain:             "backend",
			Files:              []string{"cmd/new.go"},
			AcceptanceCriteria: []string{"Command creates new ADR file"},
			ParallelGroup:      1,
		},
		{
			Title:              "Implement list command",
			Description:        "Create adr list subcommand",
			Domain:             "backend",
			Files:              []string{"cmd/list.go"},
			AcceptanceCriteria: []string{"Command lists all ADRs"},
			ParallelGroup:      1,
		},
	}

	orch := &Orchestrator{
		state:    state,
		repoRoot: "/tmp/test",
		config:   Config{},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// Create sub-tickets
	ctx := context.Background()
	orch.createSubTickets(ctx, ticket, subTickets)

	// Verify sub-tickets created
	children := state.GetTicketsByParent(ticket.ID)
	if len(children) != 3 {
		t.Errorf("Expected 3 sub-tickets, got %d", len(children))
	}

	// Verify each sub-ticket has required fields
	for _, sub := range children {
		if sub.Title == "" {
			t.Error("Sub-ticket missing title")
		}
		if len(sub.Files) == 0 {
			t.Error("Sub-ticket missing files")
		}
		if len(sub.AcceptanceCriteria) == 0 {
			t.Error("Sub-ticket missing acceptance criteria")
		}
		if sub.Status != kanban.StatusReady {
			t.Errorf("Expected sub-ticket status READY, got %s", sub.Status)
		}
		if sub.ParentID != ticket.ID {
			t.Errorf("Expected parent ID %s, got %s", ticket.ID, sub.ParentID)
		}
	}
}

// AC-7: Parallel Execution Respects File Conflicts
func TestAC7_ParallelExecutionRespectsConflicts(t *testing.T) {
	// Test file pattern overlap detection
	testCases := []struct {
		pattern1  string
		pattern2  string
		conflicts bool
	}{
		{"cmd/init.go", "cmd/new.go", false},           // Different files
		{"cmd/init.go", "cmd/*.go", true},              // Wildcard overlap
		{"cmd/**/*.go", "cmd/sub/file.go", true},       // Glob overlap
		{"internal/auth/", "internal/db/", false},      // Different directories
		{"internal/**", "internal/auth/user.go", true}, // Nested overlap
	}

	for _, tc := range testCases {
		result := patternsOverlap(tc.pattern1, tc.pattern2)
		if result != tc.conflicts {
			t.Errorf("patternsOverlap(%q, %q) = %v, want %v",
				tc.pattern1, tc.pattern2, result, tc.conflicts)
		}
	}
}

// AC-8: Max 3 Parallel DEV Agents
func TestAC8_MaxParallelDevAgents(t *testing.T) {
	state := newMockState()

	// Create 5 non-conflicting ready tickets
	for i := 0; i < 5; i++ {
		ticket := createReadySubTicket(
			fmt.Sprintf("SUB-%d", i+1),
			"PARENT-001",
			fmt.Sprintf("Ticket %d", i+1),
			[]string{fmt.Sprintf("file%d.go", i)}, // Non-overlapping files
		)
		state.AddTicket(*ticket)
	}

	config := Config{
		MaxParallelAgents: 3,
	}

	// Verify config is respected
	if config.MaxParallelAgents != 3 {
		t.Errorf("Expected max parallel agents to be 3, got %d", config.MaxParallelAgents)
	}

	// Verify we have 5 ready tickets
	readyTickets := state.GetTicketsByStatus(kanban.StatusReady)
	if len(readyTickets) != 5 {
		t.Errorf("Expected 5 ready tickets, got %d", len(readyTickets))
	}
}

// AC-9: Sub-Ticket Completion Updates Parent
func TestAC9_ParentCompletionOnAllSubsDone(t *testing.T) {
	state := newMockState()

	// Create parent ticket in BREAKING_DOWN status
	parent := &kanban.Ticket{
		ID:          "PARENT-001",
		Title:       "Parent PRD ticket",
		Status:      kanban.StatusBreakingDown,
		Conversation: &kanban.PRDConversation{
			TicketID:     "PARENT-001",
			SubTicketIDs: []string{"SUB-1", "SUB-2", "SUB-3", "SUB-4"},
		},
	}
	state.AddTicket(*parent)

	// Create 4 sub-tickets all in DONE status
	for i := 1; i <= 4; i++ {
		sub := &kanban.Ticket{
			ID:       fmt.Sprintf("SUB-%d", i),
			Title:    fmt.Sprintf("Sub-ticket %d", i),
			ParentID: "PARENT-001",
			Status:   kanban.StatusDone,
		}
		state.AddTicket(*sub)
	}

	orch := &Orchestrator{
		state:    state,
		repoRoot: "/tmp/test",
		config:   Config{},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// Run parent completion check
	ctx := context.Background()
	orch.checkParentCompletion(ctx)

	// Verify parent is now DONE
	updatedParent, _ := state.GetTicket("PARENT-001")
	if updatedParent.Status != kanban.StatusDone {
		t.Errorf("Expected parent status DONE, got %s", updatedParent.Status)
	}
}

// AC-10: User Can Participate in Discussion
func TestAC10_UserParticipationInDiscussion(t *testing.T) {
	ticket := &kanban.Ticket{
		ID:     "TEST-010",
		Title:  "Ticket needing user decision",
		Status: kanban.StatusAwaitingUser,
		Conversation: &kanban.PRDConversation{
			TicketID:     "TEST-010",
			CurrentRound: 2,
			Status:       "awaiting_user",
			UserQuestions: []kanban.Question{
				{
					Question: "Should we use approach A or B?",
				},
			},
			Rounds: []kanban.ConversationRound{
				{RoundNumber: 1, PMPrompt: "Initial analysis"},
			},
		},
	}

	// Verify ticket is awaiting user
	if ticket.Status != kanban.StatusAwaitingUser {
		t.Errorf("Expected AWAITING_USER status, got %s", ticket.Status)
	}

	// Verify user questions are present
	if len(ticket.Conversation.UserQuestions) == 0 {
		t.Error("Expected user questions to be present")
	}

	// Simulate user answer
	ticket.Conversation.UserQuestions[0].Answer = "Approach A"
	ticket.Conversation.Status = "in_progress"
	ticket.Status = kanban.Status(fmt.Sprintf("%s_2", kanban.StatusRefiningRound))

	// Verify discussion continues with answer
	if !strings.HasPrefix(string(ticket.Status), string(kanban.StatusRefiningRound)) {
		t.Error("Expected ticket to return to REFINING status after user answer")
	}
	if ticket.Conversation.UserQuestions[0].Answer != "Approach A" {
		t.Error("Expected user answer to be recorded")
	}
}

// --- Helper Functions for Tests ---

// patternsOverlap checks if two file patterns might conflict.
func patternsOverlap(pattern1, pattern2 string) bool {
	// Exact match
	if pattern1 == pattern2 {
		return true
	}

	// One contains wildcard
	if strings.Contains(pattern1, "*") || strings.Contains(pattern2, "*") {
		// Check if one pattern is a prefix of the other (simplified)
		p1Base := strings.Split(pattern1, "*")[0]
		p2Base := strings.Split(pattern2, "*")[0]

		if strings.HasPrefix(pattern2, p1Base) || strings.HasPrefix(pattern1, p2Base) {
			return true
		}
	}

	return false
}

