package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arctek/factory/agents"
	"github.com/arctek/factory/kanban"
)

// PRD Collaboration Constants
const (
	MaxPRDRounds       = 5 // Maximum discussion rounds before forcing synthesis
	MinExpertsRequired = 4 // DEV, QA, UX, Security
)

// ExpertAgents lists the domain experts involved in PRD discussions
var ExpertAgents = []string{"dev", "qa", "ux", "security"}

// processApprovedToPRDRound moves newly approved tickets into collaborative PRD refinement.
// This replaces the legacy processApprovedToRefining for the new collaborative model.
func (o *Orchestrator) processApprovedToPRDRound(ctx context.Context) {
	approvedTickets := o.state.GetTicketsByStatus(kanban.StatusApproved)
	o.logger.Info("PRD Round check", "approvedCount", len(approvedTickets))
	if len(approvedTickets) == 0 {
		return
	}

	o.logger.Info("Starting collaborative PRD discussion for approved tickets", "count", len(approvedTickets))

	for _, ticket := range approvedTickets {
		// Initialize conversation tracking
		conversation := &kanban.PRDConversation{
			TicketID:     ticket.ID,
			Rounds:       []kanban.ConversationRound{},
			CurrentRound: 1,
			Status:       "in_progress",
			StartedAt:    time.Now(),
		}

		// Update ticket with conversation
		ticket.Conversation = conversation
		o.state.UpdateTicket(&ticket)

		// Move to first refining round
		roundStatus := kanban.Status(fmt.Sprintf("%s_1", kanban.StatusRefiningRound))
		o.state.UpdateTicketStatus(ticket.ID, roundStatus, "PM", "Starting collaborative PRD discussion - Round 1")

		o.logger.Info("Ticket moved to PRD discussion", "ticket", ticket.ID, "round", 1)
	}
}

// processPRDRoundStage handles tickets in REFINING_ROUND_N status.
// It spawns the PM facilitator and all domain experts in parallel.
func (o *Orchestrator) processPRDRoundStage(ctx context.Context) {
	// Find all tickets in any REFINING_ROUND status
	allTickets, _ := o.state.GetAllTickets()
	var roundTickets []kanban.Ticket
	for _, t := range allTickets {
		if strings.HasPrefix(string(t.Status), string(kanban.StatusRefiningRound)) {
			roundTickets = append(roundTickets, t)
		}
	}

	if len(roundTickets) == 0 {
		return
	}

	o.logger.Info("Processing PRD discussion rounds", "count", len(roundTickets))

	for _, ticket := range roundTickets {
		// Check if agents are already running for this ticket
		activeRuns := o.state.GetActiveRunsForTicket(ticket.ID)
		if len(activeRuns) > 0 {
			o.logger.Info("Agents already running for ticket", "ticket", ticket.ID, "count", len(activeRuns))
			continue
		}

		// Parse current round from status (REFINING_ROUND_N -> N)
		roundNum := o.parseRoundFromStatus(ticket.Status)

		if ticket.Conversation == nil {
			ticket.Conversation = &kanban.PRDConversation{
				TicketID:     ticket.ID,
				CurrentRound: roundNum,
				Status:       "in_progress",
				StartedAt:    time.Now(),
			}
		}

		// Check if we need to initialize this round or collect responses
		currentRound := o.getCurrentRound(&ticket)

		// Check if the current round matches the expected round from status
		// This handles the case when we move to a new round but haven't created it yet
		if currentRound == nil || currentRound.RoundNumber != roundNum {
			// Start a new round - spawn PM facilitator to create the round prompt
			o.startPRDRound(ctx, &ticket, roundNum)
		} else if o.countActualResponses(currentRound) < MinExpertsRequired {
			// Round in progress - spawn any missing experts
			o.spawnMissingExperts(ctx, &ticket, currentRound)
		} else {
			// All experts responded - run PM synthesis
			o.runPMSynthesis(ctx, &ticket, currentRound)
		}
	}
}

// startPRDRound spawns the PM facilitator to create the round prompt.
func (o *Orchestrator) startPRDRound(ctx context.Context, ticket *kanban.Ticket, roundNum int) {
	o.logger.Info("Starting PRD round", "ticket", ticket.ID, "round", roundNum)
	o.state.UpdateActivity(ticket.ID, fmt.Sprintf("PM initiating round %d discussion", roundNum), "PM")

	if o.config.DryRun {
		o.logger.Info("[DRY RUN] Would spawn PM facilitator", "ticket", ticket.ID, "round", roundNum)
		return
	}

	// Prepare prompt data for PM facilitator
	promptData := agents.PromptData{
		Ticket:       ticket,
		Conversation: ticket.Conversation,
		CurrentRound: roundNum,
		BoardStats:   o.state.GetStats(),
	}
	ticketBytes, _ := json.MarshalIndent(ticket, "", "  ")
	promptData.TicketJSON = string(ticketBytes)

	// Spawn PM facilitator
	result, err := o.spawner.SpawnAgent(ctx, agents.AgentTypePMFacilitator, promptData, o.repoRoot)
	if err != nil {
		o.logger.Error("PM facilitator failed", "ticket", ticket.ID, "error", err)
		return
	}

	// Parse PM's response to get the round prompt
	roundPrompt, focusAreas := o.parsePMFacilitatorResponse(result.Output)

	// Create new conversation round
	newRound := kanban.ConversationRound{
		RoundNumber:  roundNum,
		PMPrompt:     roundPrompt,
		ExpertInputs: make(map[string]kanban.ExpertInput),
		Timestamp:    time.Now(),
	}
	ticket.Conversation.Rounds = append(ticket.Conversation.Rounds, newRound)
	ticket.Conversation.CurrentRound = roundNum
	o.state.UpdateTicket(ticket)

	// Get pointer to the actual round in the slice (not the local copy)
	roundPtr := &ticket.Conversation.Rounds[len(ticket.Conversation.Rounds)-1]

	// Now spawn all experts in parallel
	o.spawnAllExperts(ctx, ticket, roundPtr, focusAreas)
}

// spawnAllExperts spawns all domain experts in parallel for a PRD round.
func (o *Orchestrator) spawnAllExperts(ctx context.Context, ticket *kanban.Ticket, round *kanban.ConversationRound, focusAreas map[string][]string) {
	o.logger.Info("Spawning all domain experts in parallel", "ticket", ticket.ID, "round", round.RoundNumber)

	var wg sync.WaitGroup
	var mu sync.Mutex
	expertResults := make(map[string]*agents.AgentResult)

	for _, agent := range ExpertAgents {
		wg.Add(1)
		go func(agentName string) {
			defer wg.Done()

			// Prepare prompt data for expert
			promptData := agents.PromptData{
				Ticket:        ticket,
				Conversation:  ticket.Conversation,
				CurrentRound:  round.RoundNumber,
				CurrentPrompt: round.PMPrompt,
				Agent:         agentName,
				FocusAreas:    focusAreas[agentName],
				BoardStats:    o.state.GetStats(),
			}
			expertTicketBytes, _ := json.MarshalIndent(ticket, "", "  ")
			promptData.TicketJSON = string(expertTicketBytes)

			// Register the run
			run := kanban.AgentRun{
				ID:        fmt.Sprintf("prd-%s-%s-%d", ticket.ID, agentName, round.RoundNumber),
				Agent:     fmt.Sprintf("prd-%s", agentName),
				TicketID:  ticket.ID,
				StartedAt: time.Now(),
				Status:    "running",
			}
			o.state.AddRun(&run)

			// Spawn expert agent
			result, err := o.spawner.SpawnAgent(ctx, agents.AgentTypePRDExpert, promptData, o.repoRoot)

			// Complete the run
			status := "success"
			if err != nil {
				status = "failed"
				o.logger.Error("Expert agent failed", "agent", agentName, "ticket", ticket.ID, "error", err)
			}
			o.state.CompleteRun(run.ID, status, result.Output)

			mu.Lock()
			expertResults[agentName] = result
			mu.Unlock()
		}(agent)
	}

	// Wait for all experts
	wg.Wait()

	// Process results and update conversation
	for agentName, result := range expertResults {
		if result == nil || !result.Success {
			continue
		}

		input := o.parseExpertResponse(result.Output)
		input.Agent = agentName

		mu.Lock()
		round.ExpertInputs[agentName] = input
		mu.Unlock()
	}

	// Update ticket with collected inputs
	o.state.UpdateTicket(ticket)
	o.state.ClearActivity(ticket.ID)

	o.logger.Info("All experts responded", "ticket", ticket.ID, "count", len(round.ExpertInputs))
}

// spawnMissingExperts spawns any experts that haven't responded yet.
func (o *Orchestrator) spawnMissingExperts(ctx context.Context, ticket *kanban.Ticket, round *kanban.ConversationRound) {
	for _, agent := range ExpertAgents {
		if _, exists := round.ExpertInputs[agent]; !exists {
			// Check if already running
			runID := fmt.Sprintf("prd-%s-%s-%d", ticket.ID, agent, round.RoundNumber)
			activeRuns := o.state.GetActiveRuns()
			alreadyRunning := false
			for _, run := range activeRuns {
				if run.ID == runID {
					alreadyRunning = true
					break
				}
			}
			if alreadyRunning {
				continue
			}

			o.logger.Info("Spawning missing expert", "ticket", ticket.ID, "agent", agent)
			// Spawn this expert (similar to spawnAllExperts but for single agent)
			go o.spawnSingleExpert(ctx, ticket, round, agent)
		}
	}
}

// spawnSingleExpert spawns a single domain expert.
func (o *Orchestrator) spawnSingleExpert(ctx context.Context, ticket *kanban.Ticket, round *kanban.ConversationRound, agentName string) {
	promptData := agents.PromptData{
		Ticket:        ticket,
		Conversation:  ticket.Conversation,
		CurrentRound:  round.RoundNumber,
		CurrentPrompt: round.PMPrompt,
		Agent:         agentName,
		BoardStats:    o.state.GetStats(),
	}
	singleExpertBytes, _ := json.MarshalIndent(ticket, "", "  ")
	promptData.TicketJSON = string(singleExpertBytes)

	run := kanban.AgentRun{
		ID:        fmt.Sprintf("prd-%s-%s-%d", ticket.ID, agentName, round.RoundNumber),
		Agent:     fmt.Sprintf("prd-%s", agentName),
		TicketID:  ticket.ID,
		StartedAt: time.Now(),
		Status:    "running",
	}
	o.state.AddRun(&run)

	result, err := o.spawner.SpawnAgent(ctx, agents.AgentTypePRDExpert, promptData, o.repoRoot)

	status := "success"
	if err != nil {
		status = "failed"
		o.logger.Error("Expert agent failed", "agent", agentName, "ticket", ticket.ID, "error", err)
	}
	o.state.CompleteRun(run.ID, status, result.Output)

	if result != nil && result.Success {
		input := o.parseExpertResponse(result.Output)
		input.Agent = agentName
		round.ExpertInputs[agentName] = input
		o.state.UpdateTicket(ticket)
	}
}

// runPMSynthesis runs the PM to synthesize expert responses and determine next steps.
func (o *Orchestrator) runPMSynthesis(ctx context.Context, ticket *kanban.Ticket, round *kanban.ConversationRound) {
	o.logger.Info("Running PM synthesis", "ticket", ticket.ID, "round", round.RoundNumber)
	o.state.UpdateActivity(ticket.ID, "PM synthesizing expert input", "PM")

	if o.config.DryRun {
		o.logger.Info("[DRY RUN] Would run PM synthesis", "ticket", ticket.ID)
		return
	}

	// Update current round number for facilitator prompt
	nextRound := round.RoundNumber + 1

	promptData := agents.PromptData{
		Ticket:       ticket,
		Conversation: ticket.Conversation,
		CurrentRound: nextRound,
		BoardStats:   o.state.GetStats(),
	}
	synthesisTicketBytes, _ := json.MarshalIndent(ticket, "", "  ")
	promptData.TicketJSON = string(synthesisTicketBytes)

	result, err := o.spawner.SpawnAgent(ctx, agents.AgentTypePMFacilitator, promptData, o.repoRoot)
	if err != nil {
		o.logger.Error("PM synthesis failed", "ticket", ticket.ID, "error", err)
		return
	}

	// Parse PM's decision
	action, synthesis, prd := o.parsePMSynthesisResponse(result.Output)
	round.PMSynthesis = synthesis
	o.state.UpdateTicket(ticket)

	switch action {
	case "FINALIZE_PRD":
		// All experts approved - move to PRD_COMPLETE
		ticket.Conversation.Status = "consensus"
		ticket.Conversation.FinalPRD = prd
		ticket.Conversation.CompletedAt = time.Now()
		o.state.UpdateTicket(ticket)
		o.state.UpdateTicketStatus(ticket.ID, kanban.StatusPRDComplete, "PM", "All experts approved - PRD finalized")
		o.logger.Info("PRD finalized", "ticket", ticket.ID, "rounds", len(ticket.Conversation.Rounds))

	case "CONTINUE_ROUND":
		// Need another round
		if nextRound > MaxPRDRounds {
			// Force finalization with noted gaps
			ticket.Conversation.Status = "forced_consensus"
			o.state.UpdateTicket(ticket)
			o.state.UpdateTicketStatus(ticket.ID, kanban.StatusPRDComplete, "PM", fmt.Sprintf("Max rounds (%d) reached - forcing PRD synthesis", MaxPRDRounds))
		} else {
			// Move to next round
			roundStatus := kanban.Status(fmt.Sprintf("%s_%d", kanban.StatusRefiningRound, nextRound))
			o.state.UpdateTicketStatus(ticket.ID, roundStatus, "PM", fmt.Sprintf("Starting round %d based on expert feedback", nextRound))
		}

	case "REQUEST_USER_INPUT":
		// Need user decision
		ticket.Conversation.Status = "awaiting_user"
		o.state.UpdateTicket(ticket)
		o.state.UpdateTicketStatus(ticket.ID, kanban.StatusAwaitingUser, "PM", "Expert discussion requires user decision")

	default:
		o.logger.Warn("Unknown PM action", "action", action, "ticket", ticket.ID)
	}

	o.state.ClearActivity(ticket.ID)
	o.state.Save()
}

// processPRDCompleteStage handles tickets with finalized PRDs.
func (o *Orchestrator) processPRDCompleteStage(ctx context.Context) {
	prdCompleteTickets := o.state.GetTicketsByStatus(kanban.StatusPRDComplete)
	if len(prdCompleteTickets) == 0 {
		return
	}

	o.logger.Info("Processing completed PRDs for breakdown", "count", len(prdCompleteTickets))

	for _, ticket := range prdCompleteTickets {
		// Move to breaking down
		o.state.UpdateTicketStatus(ticket.ID, kanban.StatusBreakingDown, "PM", "Breaking PRD into sub-tickets")
		o.state.UpdateActivity(ticket.ID, "PM creating sub-tickets from PRD", "PM")

		if o.config.DryRun {
			o.logger.Info("[DRY RUN] Would break down PRD", "ticket", ticket.ID)
			continue
		}

		// Get the final expert inputs from last round
		var finalInputs map[string]kanban.ExpertInput
		if ticket.Conversation != nil && len(ticket.Conversation.Rounds) > 0 {
			lastRound := ticket.Conversation.Rounds[len(ticket.Conversation.Rounds)-1]
			finalInputs = lastRound.ExpertInputs
		}

		promptData := agents.PromptData{
			Ticket:            &ticket,
			Conversation:      ticket.Conversation,
			PRD:               ticket.Conversation.FinalPRD,
			FinalExpertInputs: finalInputs,
			BoardStats:        o.state.GetStats(),
		}
		breakdownTicketBytes, _ := json.MarshalIndent(ticket, "", "  ")
		promptData.TicketJSON = string(breakdownTicketBytes)

		result, err := o.spawner.SpawnAgent(ctx, agents.AgentTypePMBreakdown, promptData, o.repoRoot)
		if err != nil {
			o.logger.Error("PRD breakdown failed", "ticket", ticket.ID, "error", err)
			continue
		}

		// Parse and create sub-tickets
		subTickets := o.parsePRDBreakdownResponse(result.Output, &ticket)
		o.createSubTickets(ctx, &ticket, subTickets)

		o.state.ClearActivity(ticket.ID)
		o.state.Save()
	}
}

// createSubTickets creates sub-tickets from the PRD breakdown.
func (o *Orchestrator) createSubTickets(ctx context.Context, parent *kanban.Ticket, subTickets []SubTicketSpec) {
	if len(subTickets) == 0 {
		o.logger.Warn("No sub-tickets generated from PRD", "ticket", parent.ID)
		return
	}

	o.logger.Info("Creating sub-tickets from PRD", "parent", parent.ID, "count", len(subTickets))

	var createdIDs []string
	for i, spec := range subTickets {
		subID := fmt.Sprintf("%s-SUB-%d", parent.ID, i+1)

		subTicket := kanban.Ticket{
			ID:                 subID,
			Title:              spec.Title,
			Description:        spec.Description,
			Domain:             kanban.Domain(spec.Domain),
			Priority:           parent.Priority,
			Type:               parent.Type,
			Files:              spec.Files,
			Dependencies:       spec.Dependencies,
			AcceptanceCriteria: spec.AcceptanceCriteria,
			ParentID:           parent.ID,
			ParallelGroup:      spec.ParallelGroup,
			Status:             kanban.StatusReady,
			CreatedAt:          time.Now(),
			UpdatedAt:          time.Now(),
			Notes:              spec.TechnicalNotes,
		}

		if err := o.state.CreateTicket(&subTicket); err != nil {
			o.logger.Error("Failed to create sub-ticket", "id", subID, "error", err)
			continue
		}

		createdIDs = append(createdIDs, subID)
		o.logger.Info("Created sub-ticket", "id", subID, "title", spec.Title, "group", spec.ParallelGroup)
	}

	// Update parent with sub-ticket IDs
	parent.Conversation.SubTicketIDs = createdIDs
	o.state.UpdateTicket(parent)

	// Parent stays in BREAKING_DOWN until all sub-tickets complete
	// (handled by checkParentCompletion in regular cycle)
	o.logger.Info("Created sub-tickets from PRD", "parent", parent.ID, "count", len(createdIDs))
}

// SubTicketSpec represents a sub-ticket parsed from PM breakdown output.
type SubTicketSpec struct {
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Domain             string   `json:"domain"`
	Files              []string `json:"files"`
	Dependencies       []string `json:"dependencies"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	ParallelGroup      int      `json:"parallelGroup"`
	TechnicalNotes     string   `json:"technicalNotes"`
}

// --- Helper Functions ---

// getCurrentRound returns the current round from the conversation, or nil if none.
func (o *Orchestrator) getCurrentRound(ticket *kanban.Ticket) *kanban.ConversationRound {
	if ticket.Conversation == nil || len(ticket.Conversation.Rounds) == 0 {
		return nil
	}
	return &ticket.Conversation.Rounds[len(ticket.Conversation.Rounds)-1]
}

// countActualResponses counts expert inputs that have actual non-empty responses.
func (o *Orchestrator) countActualResponses(round *kanban.ConversationRound) int {
	count := 0
	for _, input := range round.ExpertInputs {
		if input.Response != "" {
			count++
		}
	}
	return count
}

// parseRoundFromStatus extracts the round number from REFINING_ROUND_N status.
func (o *Orchestrator) parseRoundFromStatus(status kanban.Status) int {
	// REFINING_ROUND_1 -> 1
	parts := strings.Split(string(status), "_")
	if len(parts) < 3 {
		return 1
	}
	var num int
	fmt.Sscanf(parts[2], "%d", &num)
	if num == 0 {
		return 1
	}
	return num
}

// parsePMFacilitatorResponse parses the PM facilitator's JSON output.
func (o *Orchestrator) parsePMFacilitatorResponse(output string) (string, map[string][]string) {
	// Extract JSON block from output
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return output, nil // Fall back to using the whole output as prompt
	}

	var response struct {
		Action     string              `json:"action"`
		Prompt     string              `json:"prompt"`
		FocusAreas map[string][]string `json:"focusAreas"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		o.logger.Warn("Failed to parse PM facilitator response", "error", err)
		return output, nil
	}

	return response.Prompt, response.FocusAreas
}

// parseExpertResponse parses an expert's JSON response into ExpertInput.
func (o *Orchestrator) parseExpertResponse(output string) kanban.ExpertInput {
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return kanban.ExpertInput{Response: output}
	}

	var input kanban.ExpertInput
	if err := json.Unmarshal([]byte(jsonStr), &input); err != nil {
		return kanban.ExpertInput{Response: output}
	}

	return input
}

// parsePMSynthesisResponse parses the PM's synthesis decision.
func (o *Orchestrator) parsePMSynthesisResponse(output string) (action, synthesis, prd string) {
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		o.logger.Info("No JSON found in PM output", "outputLen", len(output))
		return "", output, ""
	}

	jsonPreview := jsonStr
	if len(jsonPreview) > 200 {
		jsonPreview = jsonPreview[:200]
	}
	o.logger.Info("Extracted JSON", "json", jsonPreview)

	var response struct {
		Action    string          `json:"action"`
		Synthesis string          `json:"synthesis"`
		PRD       json.RawMessage `json:"prd"` // Capture full PRD object
	}

	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		errPreview := jsonStr
		if len(errPreview) > 500 {
			errPreview = errPreview[:500]
		}
		o.logger.Info("JSON parse error", "error", err, "json", errPreview)
		return "", output, ""
	}

	o.logger.Info("Parsed PM response", "action", response.Action, "hasSynthesis", response.Synthesis != "")

	// PRD is already JSON, just convert to string
	prdJSON := ""
	if len(response.PRD) > 0 && string(response.PRD) != "null" {
		prdJSON = string(response.PRD)
	}

	return response.Action, response.Synthesis, prdJSON
}

// parsePRDBreakdownResponse parses the PM's sub-ticket breakdown.
func (o *Orchestrator) parsePRDBreakdownResponse(output string, parent *kanban.Ticket) []SubTicketSpec {
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return nil
	}

	var response struct {
		Tickets        []SubTicketSpec `json:"tickets"`
		ParallelGroups []struct {
			Group   int      `json:"group"`
			Tickets []string `json:"tickets"`
		} `json:"parallelGroups"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		o.logger.Warn("Failed to parse breakdown response", "error", err)
		return nil
	}

	// Apply parallel groups to tickets
	groupMap := make(map[string]int)
	for _, pg := range response.ParallelGroups {
		for _, title := range pg.Tickets {
			groupMap[title] = pg.Group
		}
	}

	for i := range response.Tickets {
		if group, ok := groupMap[response.Tickets[i].Title]; ok {
			response.Tickets[i].ParallelGroup = group
		}
	}

	return response.Tickets
}

// extractJSON finds and extracts the LAST JSON block from text.
// This is important because the PM's output may include expert response JSON blocks
// as context, but the PM's decision JSON is always at the end.
func extractJSON(text string) string {
	var lastJSON string

	// Look for all ```json ... ``` blocks and keep the last one
	searchStart := 0
	for {
		idx := strings.Index(text[searchStart:], "```json")
		if idx == -1 {
			break
		}
		start := searchStart + idx + 7
		if end := strings.Index(text[start:], "```"); end != -1 {
			lastJSON = strings.TrimSpace(text[start : start+end])
			searchStart = start + end + 3
		} else {
			break
		}
	}

	if lastJSON != "" {
		return lastJSON
	}

	// Look for raw JSON object - find the LAST one
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == '}' {
			// Find matching opening brace
			depth := 0
			for j := i; j >= 0; j-- {
				if text[j] == '}' {
					depth++
				} else if text[j] == '{' {
					depth--
					if depth == 0 {
						return text[j : i+1]
					}
				}
			}
		}
	}

	return ""
}

// checkParentCompletion checks if all sub-tickets of a parent are done.
func (o *Orchestrator) checkParentCompletion(ctx context.Context) {
	// Find tickets in BREAKING_DOWN status
	breakingDownTickets := o.state.GetTicketsByStatus(kanban.StatusBreakingDown)

	for _, parent := range breakingDownTickets {
		if parent.Conversation == nil || len(parent.Conversation.SubTicketIDs) == 0 {
			continue
		}

		// Check if all sub-tickets are done
		allDone := true
		for _, subID := range parent.Conversation.SubTicketIDs {
			sub, found := o.state.GetTicket(subID)
			if !found || sub.Status != kanban.StatusDone {
				allDone = false
				break
			}
		}

		if allDone {
			o.state.UpdateTicketStatus(parent.ID, kanban.StatusDone, "system", "All sub-tickets completed")
			o.logger.Info("Parent ticket completed", "ticket", parent.ID)
		}
	}
}
