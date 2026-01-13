# Collaborative PRD Creation Model

> **Status**: Planning
> **Created**: 2026-01-10
> **Goal**: Enable multi-round facilitated discussions between PM and domain experts (DEV, QA, UX, Security) to collaboratively build PRDs before implementation.

---

## Overview

The current Factory has a linear flow where PM analyzes requirements, optionally consults one expert, then moves to development. This misses the collaborative nature of real product development where all stakeholders contribute iteratively.

### Current Flow (Linear)
```
APPROVED → PM analyzes → NEEDS_EXPERT → one expert → AWAITING_USER → READY → DEV
```

### Target Flow (Collaborative)
```
APPROVED → PM facilitates multi-round discussion with ALL experts → PRD complete →
PM breaks into sub-tickets → Multiple DEVs work in parallel → Review pipeline
```

---

## Detailed Design

### Phase 1: Collaborative PRD Discussion

#### 1.1 Discussion Model

```
┌─────────────────────────────────────────────────────────────────┐
│                        PM FACILITATOR                           │
│  - Initiates discussion rounds                                  │
│  - Synthesizes expert input                                     │
│  - Maintains conversation context                               │
│  - Decides when consensus reached                               │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
        ┌─────────────────────────────────────────────┐
        │           DISCUSSION ROUND N                 │
        │                                              │
        │  Context: All previous rounds + synthesis    │
        │                                              │
        │  ┌─────┐  ┌─────┐  ┌─────┐  ┌──────────┐   │
        │  │ DEV │  │ QA  │  │ UX  │  │ SECURITY │   │
        │  └──┬──┘  └──┬──┘  └──┬──┘  └────┬─────┘   │
        │     │        │        │          │         │
        │     └────────┴────────┴──────────┘         │
        │                    │                        │
        │                    ▼                        │
        │            PM SYNTHESIZES                   │
        └─────────────────────────────────────────────┘
                              │
                              ▼
                    Round N+1 or PRD Complete
```

#### 1.2 Ticket Status Flow

```
APPROVED
    │
    ▼
REFINING_ROUND_1    ← PM initiates, spawns all 4 experts in parallel
    │
    ▼
REFINING_ROUND_N    ← PM synthesizes, spawns again with full context
    │                  (repeats until consensus)
    ▼
PRD_COMPLETE        ← PM finalizes PRD document
    │
    ▼
BREAKING_DOWN       ← PM creates sub-tickets from PRD
    │
    ▼
(Sub-tickets created with status READY)
```

#### 1.3 Conversation Context Structure

```go
type ConversationRound struct {
    RoundNumber   int                    `json:"roundNumber"`
    PMPrompt      string                 `json:"pmPrompt"`      // What PM asked this round
    ExpertInputs  map[string]ExpertInput `json:"expertInputs"`  // Responses from each expert
    PMSynthesis   string                 `json:"pmSynthesis"`   // PM's summary after round
    Timestamp     time.Time              `json:"timestamp"`
}

type ExpertInput struct {
    Agent       string   `json:"agent"`       // "dev", "qa", "ux", "security"
    Response    string   `json:"response"`    // Full response text
    KeyPoints   []string `json:"keyPoints"`   // Extracted key points
    Concerns    []string `json:"concerns"`    // Any concerns raised
    Approves    bool     `json:"approves"`    // Ready to proceed?
}

type PRDConversation struct {
    TicketID    string              `json:"ticketId"`
    Rounds      []ConversationRound `json:"rounds"`
    Status      string              `json:"status"`      // "in_progress", "consensus", "blocked"
    FinalPRD    string              `json:"finalPrd"`    // Final PRD document
    SubTickets  []string            `json:"subTickets"`  // Created sub-ticket IDs
}
```

#### 1.4 Expert Prompts (Updated)

Each expert receives the FULL conversation history:

```markdown
# {{.Agent}} Domain Expert - PRD Collaboration

## Ticket
{{.TicketJSON}}

## Conversation History
{{range .Conversation.Rounds}}
### Round {{.RoundNumber}}

**PM Asked:**
{{.PMPrompt}}

**Expert Responses:**
{{range $agent, $input := .ExpertInputs}}
- **{{$agent}}**: {{$input.Response}}
{{end}}

**PM Synthesis:**
{{.PMSynthesis}}

---
{{end}}

## Current Round: {{.CurrentRound}}

**PM's Current Question:**
{{.CurrentPrompt}}

## Your Task

As the {{.Agent}} expert, provide your perspective on:
1. Technical feasibility (if DEV)
2. Testability and quality concerns (if QA)
3. User experience and accessibility (if UX)
4. Security risks and mitigations (if SECURITY)

Consider what other experts said in previous rounds. Build on their input.
Identify any concerns or conflicts with other perspectives.

## Output Format

```json
{
  "keyPoints": ["Your main points"],
  "concerns": ["Any concerns or risks"],
  "questionsForOthers": ["Questions for other experts"],
  "approves": true/false,
  "reasoning": "Why you approve or what's blocking"
}
```
```

### Phase 2: PRD Breakdown

#### 2.1 PM Creates Sub-Tickets

After PRD is complete, PM analyzes it and creates implementation tickets:

```markdown
# PM - PRD Breakdown

## Completed PRD
{{.FinalPRD}}

## Conversation Summary
{{.ConversationSummary}}

## Your Task

Break this PRD into small, parallelizable implementation tickets.

### Rules
1. Each ticket must be completable in ONE agent session
2. Identify file patterns to detect conflicts (tickets touching same files can't run in parallel)
3. Mark dependencies (ticket B depends on ticket A)
4. Assign domain: frontend, backend, infra
5. Include acceptance criteria from the PRD

### Output Format

```json
{
  "tickets": [
    {
      "title": "Implement adr init command",
      "description": "...",
      "domain": "backend",
      "files": ["agents/adr-cli/cmd/init.go", "agents/adr-cli/internal/**"],
      "dependencies": [],
      "acceptanceCriteria": ["..."],
      "estimatedSize": "small|medium"
    }
  ],
  "parallelGroups": [
    ["ticket-1", "ticket-2"],  // Can run together
    ["ticket-3"]               // Must wait for group 1
  ]
}
```
```

#### 2.2 Conflict Detection for Parallelism

```go
// CanRunInParallel checks if two tickets can be worked on simultaneously
func CanRunInParallel(t1, t2 *Ticket) bool {
    // Check file pattern overlap
    for _, pattern1 := range t1.Files {
        for _, pattern2 := range t2.Files {
            if patternsOverlap(pattern1, pattern2) {
                return false
            }
        }
    }

    // Check dependencies
    if slices.Contains(t1.Dependencies, t2.ID) || slices.Contains(t2.Dependencies, t1.ID) {
        return false
    }

    return true
}
```

---

## Acceptance Criteria

### AC-1: Multi-Round Discussion Initiation
**Given** a ticket in APPROVED status
**When** the orchestrator picks it up
**Then** PM initiates Round 1 by spawning DEV, QA, UX, Security agents in parallel

**Test:**
```go
func TestPRDDiscussionInitiation(t *testing.T) {
    orch := newTestOrchestrator()
    ticket := createApprovedTicket("Test Feature")

    orch.runCycle()

    // Verify 4 agents spawned in parallel
    assert.Equal(t, 4, len(orch.spawner.ActiveAgents()))
    assert.Contains(t, orch.spawner.AgentTypes(), "dev")
    assert.Contains(t, orch.spawner.AgentTypes(), "qa")
    assert.Contains(t, orch.spawner.AgentTypes(), "ux")
    assert.Contains(t, orch.spawner.AgentTypes(), "security")

    // Verify ticket status
    assert.Equal(t, StatusRefiningRound1, ticket.Status)
}
```

### AC-2: Expert Receives Full Context
**Given** a discussion in Round N (N > 1)
**When** an expert agent is spawned
**Then** it receives all previous rounds' prompts, responses, and syntheses

**Test:**
```go
func TestExpertReceivesFullContext(t *testing.T) {
    orch := newTestOrchestrator()
    ticket := createTicketInRound(3) // Round 3 of discussion

    // Add 2 rounds of history
    ticket.Conversation.Rounds = []ConversationRound{
        {RoundNumber: 1, PMPrompt: "Initial analysis", ...},
        {RoundNumber: 2, PMPrompt: "Follow-up", ...},
    }

    prompt := orch.buildExpertPrompt(ticket, "dev")

    // Verify all rounds included
    assert.Contains(t, prompt, "Round 1")
    assert.Contains(t, prompt, "Round 2")
    assert.Contains(t, prompt, "Initial analysis")
    assert.Contains(t, prompt, "Follow-up")

    // Verify other experts' responses included
    assert.Contains(t, prompt, "QA:")
    assert.Contains(t, prompt, "Security:")
}
```

### AC-3: PM Synthesizes After Each Round
**Given** all 4 experts have responded in Round N
**When** PM runs synthesis
**Then** PM creates a summary that includes all key points, concerns, and questions

**Test:**
```go
func TestPMSynthesizesRound(t *testing.T) {
    orch := newTestOrchestrator()
    ticket := createTicketWithExpertResponses()

    synthesis := orch.runPMSynthesis(ticket)

    // Verify synthesis captures all input
    assert.Contains(t, synthesis, "DEV recommends")
    assert.Contains(t, synthesis, "QA concerns")
    assert.Contains(t, synthesis, "UX requirements")
    assert.Contains(t, synthesis, "Security considerations")

    // Verify next action determined
    assert.NotEmpty(t, synthesis.NextAction) // "another_round" or "prd_complete"
}
```

### AC-4: Consensus Detection
**Given** all experts approve in a round
**When** PM evaluates responses
**Then** discussion moves to PRD_COMPLETE status

**Test:**
```go
func TestConsensusDetection(t *testing.T) {
    orch := newTestOrchestrator()
    ticket := createTicketWithAllApprovals()

    orch.runCycle()

    assert.Equal(t, StatusPRDComplete, ticket.Status)
    assert.NotEmpty(t, ticket.Conversation.FinalPRD)
}
```

### AC-5: Continued Discussion on Concerns
**Given** at least one expert has concerns
**When** PM evaluates responses
**Then** another round is initiated with those concerns highlighted

**Test:**
```go
func TestContinuedDiscussionOnConcerns(t *testing.T) {
    orch := newTestOrchestrator()
    ticket := createTicketWithSecurityConcern()

    orch.runCycle()

    assert.Equal(t, StatusRefiningRound2, ticket.Status)
    assert.Contains(t, ticket.Conversation.Rounds[1].PMPrompt, "Security raised concern")
}
```

### AC-6: PRD Breakdown Creates Sub-Tickets
**Given** a ticket with PRD_COMPLETE status
**When** PM runs breakdown
**Then** multiple sub-tickets are created with proper dependencies and file patterns

**Test:**
```go
func TestPRDBreakdown(t *testing.T) {
    orch := newTestOrchestrator()
    ticket := createCompletedPRDTicket()
    ticket.Conversation.FinalPRD = "..." // ADR CLI PRD

    orch.runCycle()

    subTickets := orch.state.GetTicketsByParent(ticket.ID)

    assert.GreaterOrEqual(t, len(subTickets), 3) // At least 3 sub-tickets

    // Verify each has required fields
    for _, sub := range subTickets {
        assert.NotEmpty(t, sub.Title)
        assert.NotEmpty(t, sub.Files)
        assert.NotEmpty(t, sub.AcceptanceCriteria)
        assert.Equal(t, StatusReady, sub.Status)
        assert.Equal(t, ticket.ID, sub.ParentID)
    }
}
```

### AC-7: Parallel Execution Respects File Conflicts
**Given** 3 READY sub-tickets where 2 touch overlapping files
**When** orchestrator picks up work
**Then** only non-conflicting tickets run in parallel

**Test:**
```go
func TestParallelExecutionRespectsConflicts(t *testing.T) {
    orch := newTestOrchestrator()

    // Create 3 tickets
    t1 := createReadyTicket("Init cmd", []string{"cmd/init.go"})
    t2 := createReadyTicket("New cmd", []string{"cmd/new.go"})
    t3 := createReadyTicket("Shared types", []string{"cmd/*.go"}) // Conflicts with t1, t2

    orch.runCycle()

    activeDevs := orch.getActiveDevAgents()

    // t1 and t2 can run together, but not t3
    if containsTicket(activeDevs, t3) {
        assert.Equal(t, 1, len(activeDevs)) // Only t3
    } else {
        assert.LessOrEqual(t, len(activeDevs), 2) // t1 and/or t2
        assert.NotContains(t, activeDevs, t3)
    }
}
```

### AC-8: Max 3 Parallel DEV Agents
**Given** 5 READY tickets with no conflicts
**When** orchestrator picks up work
**Then** only 3 DEV agents run concurrently

**Test:**
```go
func TestMaxParallelDevAgents(t *testing.T) {
    orch := newTestOrchestrator()
    orch.config.MaxParallelAgents = 3

    // Create 5 non-conflicting tickets
    for i := 0; i < 5; i++ {
        createReadyTicket(fmt.Sprintf("Ticket %d", i), []string{fmt.Sprintf("file%d.go", i)})
    }

    orch.runCycle()

    assert.Equal(t, 3, len(orch.getActiveDevAgents()))
}
```

### AC-9: Sub-Ticket Completion Updates Parent
**Given** a parent PRD ticket with 4 sub-tickets
**When** all sub-tickets reach DONE
**Then** parent ticket is marked DONE

**Test:**
```go
func TestParentCompletionOnAllSubsDone(t *testing.T) {
    orch := newTestOrchestrator()
    parent := createPRDTicketWithSubTickets(4)

    // Complete all sub-tickets
    for _, sub := range parent.SubTickets {
        orch.state.UpdateTicketStatus(sub.ID, StatusDone, "test", "completed")
    }

    orch.runCycle()

    assert.Equal(t, StatusDone, parent.Status)
}
```

### AC-10: User Can Participate in Discussion
**Given** a discussion where PM needs user input
**When** PM determines user decision required
**Then** ticket moves to AWAITING_USER with specific questions

**Test:**
```go
func TestUserParticipationInDiscussion(t *testing.T) {
    orch := newTestOrchestrator()
    ticket := createTicketNeedingUserDecision()

    orch.runCycle()

    assert.Equal(t, StatusAwaitingUser, ticket.Status)
    assert.NotEmpty(t, ticket.Conversation.UserQuestions)

    // User answers via API
    orch.answerUserQuestions(ticket.ID, map[string]string{
        "q1": "Use approach A",
    })

    orch.runCycle()

    // Back to discussion with user's answer in context
    assert.Contains(t, ticket.Status, "REFINING")
    assert.Contains(t, ticket.Conversation.LastRound().PMPrompt, "User decided: Use approach A")
}
```

---

## Database Schema Changes

```sql
-- Add conversation tracking to tickets
ALTER TABLE tickets ADD COLUMN conversation TEXT; -- JSON blob

-- Add parent-child relationship
ALTER TABLE tickets ADD COLUMN parent_id TEXT REFERENCES tickets(id);

-- Add parallel group tracking
ALTER TABLE tickets ADD COLUMN parallel_group INTEGER;

-- Index for finding sub-tickets
CREATE INDEX idx_tickets_parent ON tickets(parent_id);
```

---

## Implementation Phases

### Phase 1: Core Infrastructure
- [ ] Add `PRDConversation` type to kanban/types.go
- [ ] Add `conversation` column to SQLite schema
- [ ] Update `StateStore` interface with conversation methods
- [ ] Add `parent_id` support for sub-tickets

### Phase 2: Multi-Round Discussion
- [ ] Create `pm-facilitator.md` prompt for initiating rounds
- [ ] Update expert prompts to receive full context
- [ ] Implement parallel expert spawning in orchestrator
- [ ] Implement PM synthesis logic
- [ ] Add consensus detection

### Phase 3: PRD Breakdown
- [ ] Create `pm-breakdown.md` prompt
- [ ] Implement sub-ticket creation from PM output
- [ ] Implement file pattern conflict detection
- [ ] Add parallel group assignment

### Phase 4: Orchestrator Integration
- [ ] Add `processRefiningStage` to handle multi-round discussions
- [ ] Update `processDevStage` for parallel execution with conflict awareness
- [ ] Add parent ticket completion tracking
- [ ] Integrate with existing pipeline (QA, UX, Security, PM review)

### Phase 5: Testing
- [ ] Unit tests for all AC
- [ ] Integration test: Full flow from APPROVED to DONE
- [ ] Load test: 3 parallel DEV agents

---

## Success Metrics

1. **Collaboration Quality**: PRDs include input from all 4 domains
2. **Parallelism**: Average 2.5+ DEV agents running concurrently
3. **Conflict Prevention**: Zero merge conflicts from parallel work
4. **Cycle Time**: Tickets complete 3x faster with parallel DEVs
5. **Context Retention**: Experts reference each other's input 80%+ of rounds

---

## Open Questions

1. **Max Rounds**: Should there be a limit on discussion rounds? (Suggest: 5)
2. **Timeout**: What if an expert agent fails mid-discussion?
3. **User Override**: Can user force-approve a PRD even with expert concerns?
4. **Partial Consensus**: What if 3/4 experts approve but 1 has blocking concern?
