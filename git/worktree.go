// Package git provides git operations for the AI development factory,
// including worktree management for parallel agent work.
package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	repoRoot    string // Main repository root
	worktreeDir string // Directory for worktrees (e.g., .worktrees)
	mainBranch  string // Main branch name (e.g., main)
	bareRepo    string // Optional bare repo path for local-only workflow
}

// NewWorktreeManager creates a new worktree manager.
func NewWorktreeManager(repoRoot, worktreeDir, mainBranch string) *WorktreeManager {
	return &WorktreeManager{
		repoRoot:    repoRoot,
		worktreeDir: worktreeDir,
		mainBranch:  mainBranch,
	}
}

// SetBareRepo configures a local bare repo for worktree operations.
// This enables local-only workflow without needing remote access.
func (m *WorktreeManager) SetBareRepo(bareRepoPath string) {
	m.bareRepo = bareRepoPath
}

// WorktreeInfo contains information about a worktree.
type WorktreeInfo struct {
	Path   string // Absolute path to worktree
	Branch string // Branch name
	Commit string // Current commit hash
	Bare   bool   // Is this the bare repo?
}

// CreateWorktree creates a new worktree for a ticket.
// Returns the absolute path to the worktree.
func (m *WorktreeManager) CreateWorktree(ticketID, branchName string) (string, error) {
	// Sanitize branch name for filesystem
	safeName := sanitizeBranchName(branchName)

	// Determine which repo to use for worktrees
	sourceRepo := m.repoRoot
	if m.bareRepo != "" {
		sourceRepo = m.bareRepo
	}

	// Build worktree path (use absolute path for bare repo compatibility)
	worktreePath := filepath.Join(m.repoRoot, m.worktreeDir, safeName)
	absWorktreePath, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	worktreePath = absWorktreePath

	// Ensure worktree directory exists
	worktreeParent := filepath.Dir(worktreePath)
	if err := os.MkdirAll(worktreeParent, 0750); err != nil {
		return "", fmt.Errorf("failed to create worktree directory: %w", err)
	}

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		// Worktree exists, just return the path
		return worktreePath, nil
	}

	// Only fetch if not using bare repo (bare repo is local, no fetch needed)
	if m.bareRepo == "" {
		if err := m.runGit(m.repoRoot, "fetch", "origin", m.mainBranch); err != nil {
			return "", fmt.Errorf("failed to fetch origin: %w", err)
		}
	}

	// Check if branch exists
	branchExists := m.branchExistsIn(sourceRepo, branchName)

	var args []string
	if branchExists {
		// Checkout existing branch
		args = []string{"worktree", "add", worktreePath, branchName}
	} else {
		// Create new branch from main (local or origin depending on mode)
		if m.bareRepo != "" {
			// Use local main branch from bare repo
			args = []string{"worktree", "add", "-b", branchName, worktreePath, m.mainBranch}
		} else {
			// Use origin/main
			args = []string{"worktree", "add", "-b", branchName, worktreePath, "origin/" + m.mainBranch}
		}
	}

	if err := m.runGit(sourceRepo, args...); err != nil {
		return "", fmt.Errorf("failed to create worktree: %w", err)
	}

	return worktreePath, nil
}

// RemoveWorktree removes a worktree and optionally its branch.
func (m *WorktreeManager) RemoveWorktree(worktreePath string, removeBranch bool) error {
	// Get branch name before removing
	var branchName string
	if removeBranch {
		info, err := m.GetWorktreeInfo(worktreePath)
		if err == nil {
			branchName = info.Branch
		}
	}

	// Remove worktree
	if err := m.runGit(m.repoRoot, "worktree", "remove", "--force", worktreePath); err != nil {
		// Try manual removal if git worktree remove fails
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("failed to remove worktree directory: %w", rmErr)
		}
		// Prune worktrees (ignore error - best effort cleanup)
		_ = m.runGit(m.repoRoot, "worktree", "prune")
	}

	// Remove branch if requested (ignore error - best effort cleanup)
	if removeBranch && branchName != "" && branchName != m.mainBranch {
		_ = m.runGit(m.repoRoot, "branch", "-D", branchName)
	}

	return nil
}

// ListWorktrees returns all worktrees in the worktree directory.
func (m *WorktreeManager) ListWorktrees() ([]WorktreeInfo, error) {
	output, err := m.runGitOutput(m.repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	var worktrees []WorktreeInfo
	var current *WorktreeInfo

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if current != nil {
				worktrees = append(worktrees, *current)
				current = nil
			}
			continue
		}

		if strings.HasPrefix(line, "worktree ") {
			current = &WorktreeInfo{
				Path: strings.TrimPrefix(line, "worktree "),
			}
		} else if strings.HasPrefix(line, "HEAD ") && current != nil {
			current.Commit = strings.TrimPrefix(line, "HEAD ")
		} else if strings.HasPrefix(line, "branch ") && current != nil {
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		} else if line == "bare" && current != nil {
			current.Bare = true
		}
	}

	if current != nil {
		worktrees = append(worktrees, *current)
	}

	return worktrees, nil
}

// GetWorktreeInfo returns info about a specific worktree.
func (m *WorktreeManager) GetWorktreeInfo(worktreePath string) (*WorktreeInfo, error) {
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return nil, err
	}

	absPath, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}
	for _, wt := range worktrees {
		wtAbs, err := filepath.Abs(wt.Path)
		if err != nil {
			continue // Skip worktrees with invalid paths
		}
		if wtAbs == absPath {
			return &wt, nil
		}
	}

	return nil, fmt.Errorf("worktree not found: %s", worktreePath)
}

// UpdateWorktree rebases the worktree on the latest main.
func (m *WorktreeManager) UpdateWorktree(worktreePath string) error {
	// Fetch latest
	if err := m.runGit(worktreePath, "fetch", "origin", m.mainBranch); err != nil {
		return fmt.Errorf("failed to fetch: %w", err)
	}

	// Check for uncommitted changes
	output, err := m.runGitOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("failed to check status: %w", err)
	}
	if len(bytes.TrimSpace(output)) > 0 {
		return fmt.Errorf("worktree has uncommitted changes")
	}

	// Rebase on origin/main
	if err := m.runGit(worktreePath, "rebase", "origin/"+m.mainBranch); err != nil {
		// Abort rebase on failure
		_ = m.runGit(worktreePath, "rebase", "--abort")
		return fmt.Errorf("rebase failed: %w", err)
	}

	return nil
}

// SquashMerge merges a branch into main using squash merge.
func (m *WorktreeManager) SquashMerge(branchName, commitMessage string) error {
	// Switch to main in the main repo
	if err := m.runGit(m.repoRoot, "checkout", m.mainBranch); err != nil {
		return fmt.Errorf("failed to checkout main: %w", err)
	}

	// Pull latest
	if err := m.runGit(m.repoRoot, "pull", "origin", m.mainBranch); err != nil {
		return fmt.Errorf("failed to pull main: %w", err)
	}

	// Squash merge
	if err := m.runGit(m.repoRoot, "merge", "--squash", branchName); err != nil {
		return fmt.Errorf("failed to squash merge: %w", err)
	}

	// Commit
	if err := m.runGit(m.repoRoot, "commit", "-m", commitMessage); err != nil {
		return fmt.Errorf("failed to commit merge: %w", err)
	}

	return nil
}

// PushMain pushes main to origin.
func (m *WorktreeManager) PushMain() error {
	return m.runGit(m.repoRoot, "push", "origin", m.mainBranch)
}

// Commit commits changes in a worktree.
func (m *WorktreeManager) Commit(worktreePath, message string) error {
	// Stage all changes
	if err := m.runGit(worktreePath, "add", "-A"); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}

	// Check if there's anything to commit
	output, err := m.runGitOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("failed to check status: %w", err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return nil // Nothing to commit
	}

	// Commit
	if err := m.runGit(worktreePath, "commit", "-m", message); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	return nil
}

// Push pushes the current branch to origin.
func (m *WorktreeManager) Push(worktreePath string) error {
	// Get current branch
	output, err := m.runGitOutput(worktreePath, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(output))

	// Push with upstream tracking
	if err := m.runGit(worktreePath, "push", "-u", "origin", branch); err != nil {
		return fmt.Errorf("failed to push: %w", err)
	}

	return nil
}

// HasUncommittedChanges checks if a worktree has uncommitted changes.
func (m *WorktreeManager) HasUncommittedChanges(worktreePath string) (bool, error) {
	output, err := m.runGitOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(output)) > 0, nil
}

// GetCurrentBranch returns the current branch of a worktree.
func (m *WorktreeManager) GetCurrentBranch(worktreePath string) (string, error) {
	output, err := m.runGitOutput(worktreePath, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// GetLatestCommit returns the latest commit hash.
func (m *WorktreeManager) GetLatestCommit(worktreePath string) (string, error) {
	output, err := m.runGitOutput(worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// CleanupOrphanedWorktrees removes worktrees that are no longer tracked.
func (m *WorktreeManager) CleanupOrphanedWorktrees() error {
	return m.runGit(m.repoRoot, "worktree", "prune")
}

// branchExistsIn checks if a branch exists in a specific repo.
func (m *WorktreeManager) branchExistsIn(repoPath, branchName string) bool {
	// Check local
	err := m.runGit(repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	if err == nil {
		return true
	}

	// Check remote (only if not a bare repo)
	if m.bareRepo == "" {
		err = m.runGit(repoPath, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branchName)
		return err == nil
	}

	return false
}

// runGit runs a git command in the specified directory.
func (m *WorktreeManager) runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runGitOutput runs a git command and returns its output.
func (m *WorktreeManager) runGitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// sanitizeBranchName converts a branch name to a safe directory name.
func sanitizeBranchName(branch string) string {
	// Remove feat/ prefix if present
	branch = strings.TrimPrefix(branch, "feat/")
	branch = strings.TrimPrefix(branch, "fix/")
	branch = strings.TrimPrefix(branch, "chore/")

	// Replace unsafe characters
	re := regexp.MustCompile(`[^a-zA-Z0-9-_]`)
	return re.ReplaceAllString(branch, "-")
}

// GenerateBranchName creates a branch name from a ticket ID and title.
func GenerateBranchName(prefix, ticketID, title string) string {
	// Sanitize title
	re := regexp.MustCompile(`[^a-zA-Z0-9\s-]`)
	title = re.ReplaceAllString(title, "")
	title = strings.ToLower(title)
	title = strings.ReplaceAll(title, " ", "-")

	// Truncate if too long
	if len(title) > 40 {
		title = title[:40]
	}

	// Remove trailing dashes
	title = strings.TrimRight(title, "-")

	return fmt.Sprintf("%s%s-%s", prefix, ticketID, title)
}
