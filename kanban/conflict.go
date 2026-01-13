package kanban

import (
	"path/filepath"
	"strings"
)

// HasConflict checks if a ticket conflicts with any in-progress work.
// A conflict exists when file patterns overlap between tickets.
func (s *State) HasConflict(ticket *Ticket) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasConflictUnsafe(ticket)
}

// hasConflictUnsafe checks conflicts without locking (for internal use).
func (s *State) hasConflictUnsafe(ticket *Ticket) bool {
	// Get all in-progress tickets
	inProgressStatuses := []Status{StatusInDev, StatusInQA, StatusInUX, StatusInSec}

	for _, other := range s.board.Tickets {
		// Skip self
		if other.ID == ticket.ID {
			continue
		}

		// Check if other ticket is in progress
		inProgress := false
		for _, status := range inProgressStatuses {
			if other.Status == status {
				inProgress = true
				break
			}
		}
		if !inProgress {
			continue
		}

		// Check for file overlap
		if filesOverlap(ticket.Files, other.Files) {
			return true
		}
	}

	return false
}

// GetConflictingTickets returns all tickets that conflict with the given ticket.
func (s *State) GetConflictingTickets(ticket *Ticket) []Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var conflicts []Ticket
	inProgressStatuses := []Status{StatusInDev, StatusInQA, StatusInUX, StatusInSec}

	for _, other := range s.board.Tickets {
		if other.ID == ticket.ID {
			continue
		}

		inProgress := false
		for _, status := range inProgressStatuses {
			if other.Status == status {
				inProgress = true
				break
			}
		}
		if !inProgress {
			continue
		}

		if filesOverlap(ticket.Files, other.Files) {
			conflicts = append(conflicts, other)
		}
	}

	return conflicts
}

// filesOverlap checks if any patterns from a overlap with patterns from b.
func filesOverlap(a, b []string) bool {
	for _, patternA := range a {
		for _, patternB := range b {
			if patternsOverlap(patternA, patternB) {
				return true
			}
		}
	}
	return false
}

// patternsOverlap checks if two glob patterns could match the same files.
// This is a conservative check - it may return true even if patterns don't actually overlap.
func patternsOverlap(a, b string) bool {
	// Normalize paths
	a = filepath.Clean(a)
	b = filepath.Clean(b)

	// Direct match
	if a == b {
		return true
	}

	// Check if one is a parent directory of the other
	if isParentPath(a, b) || isParentPath(b, a) {
		return true
	}

	// Check if patterns share a common prefix
	aParts := strings.Split(a, string(filepath.Separator))
	bParts := strings.Split(b, string(filepath.Separator))

	// Find common prefix length
	minLen := len(aParts)
	if len(bParts) < minLen {
		minLen = len(bParts)
	}

	commonPrefixLen := 0
	for i := 0; i < minLen; i++ {
		if aParts[i] == bParts[i] || aParts[i] == "*" || bParts[i] == "*" ||
			aParts[i] == "**" || bParts[i] == "**" {
			commonPrefixLen++
		} else {
			break
		}
	}

	// If we matched all parts of the shorter pattern, they might overlap
	if commonPrefixLen == minLen {
		return true
	}

	// Check for ** which matches any depth
	if strings.Contains(a, "**") || strings.Contains(b, "**") {
		// Conservative: if either has **, check if they share any directory
		aDir := getFirstConcreteDir(a)
		bDir := getFirstConcreteDir(b)
		if aDir != "" && bDir != "" && (aDir == bDir || strings.HasPrefix(aDir, bDir) || strings.HasPrefix(bDir, aDir)) {
			return true
		}
	}

	return false
}

// isParentPath checks if parent is a parent directory of child.
func isParentPath(parent, child string) bool {
	// Handle glob patterns
	parent = strings.TrimSuffix(parent, "/*")
	parent = strings.TrimSuffix(parent, "/**")

	child = strings.TrimSuffix(child, "/*")
	child = strings.TrimSuffix(child, "/**")

	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// getFirstConcreteDir returns the first non-glob directory component.
func getFirstConcreteDir(pattern string) string {
	parts := strings.Split(pattern, string(filepath.Separator))
	for _, part := range parts {
		if part != "*" && part != "**" && !strings.Contains(part, "*") {
			return part
		}
	}
	return ""
}

// ConflictMatrix returns a matrix showing which tickets conflict with each other.
// Useful for visualization and debugging.
func (s *State) ConflictMatrix() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matrix := make(map[string][]string)

	for i, a := range s.board.Tickets {
		for j, b := range s.board.Tickets {
			if i >= j {
				continue // Only check each pair once
			}
			if filesOverlap(a.Files, b.Files) {
				matrix[a.ID] = append(matrix[a.ID], b.ID)
				matrix[b.ID] = append(matrix[b.ID], a.ID)
			}
		}
	}

	return matrix
}

// SuggestParallelGroups suggests groups of tickets that can be worked on in parallel.
// Returns groups where no tickets within a group conflict with each other.
func (s *State) SuggestParallelGroups(tickets []Ticket) [][]Ticket {
	if len(tickets) == 0 {
		return nil
	}

	var groups [][]Ticket
	used := make(map[string]bool)

	for _, ticket := range tickets {
		if used[ticket.ID] {
			continue
		}

		// Start a new group with this ticket
		group := []Ticket{ticket}
		used[ticket.ID] = true

		// Try to add more tickets to this group
		for _, candidate := range tickets {
			if used[candidate.ID] {
				continue
			}

			// Check if candidate conflicts with any ticket in the group
			conflicts := false
			for _, member := range group {
				if filesOverlap(candidate.Files, member.Files) {
					conflicts = true
					break
				}
			}

			if !conflicts {
				group = append(group, candidate)
				used[candidate.ID] = true
			}
		}

		groups = append(groups, group)
	}

	return groups
}

// ValidateTicketFiles checks that a ticket's file patterns are valid.
func ValidateTicketFiles(files []string) []string {
	var errors []string

	for _, pattern := range files {
		// Check for empty patterns
		if pattern == "" {
			errors = append(errors, "empty file pattern")
			continue
		}

		// Check for dangerous patterns
		if pattern == "/" || pattern == "/*" || pattern == "/**" {
			errors = append(errors, "pattern too broad: "+pattern)
			continue
		}

		// Check for absolute paths (should be relative)
		if filepath.IsAbs(pattern) {
			errors = append(errors, "pattern should be relative: "+pattern)
		}
	}

	return errors
}
