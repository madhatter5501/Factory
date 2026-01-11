<!--
  Agent:       shared-rules
  Type:        Template Include
  Invoked By:  All worktree-based agents (dev-*, qa, security, ux, merge)
  Purpose:     Common rules for worktree isolation, tooling discovery, and output protocol
  Template:    Include via {{template "shared-rules.md" .}}
-->

# Shared Rules for All Agents

This file contains common rules and context included in all agent prompts.

## Worktree Isolation

**CRITICAL**: You are working in an isolated git worktree at:
```
{{.WorktreePath}}
```

**IMPORTANT RULES**:
- **STAY IN THIS DIRECTORY** - Do not navigate outside `{{.WorktreePath}}`
- **ALL commits go here** - This worktree has its own branch
- **Do NOT edit kanban.json** - The orchestrator handles state updates automatically
- The worktree is a full copy of the codebase on branch: `feat/{{.Ticket.ID}}-*`

## Ticket Context

Your ticket provides all the context you need:

```json
{{.TicketJSON}}
```

**Key fields to use:**
- `technical_context.affected_paths` - Where to make changes
- `technical_context.patterns_to_follow` - Examples to reference
- `technical_context.stack` - Technologies in use
- `acceptance_criteria` - Definition of done
- `constraints` - What NOT to do

## Tooling Discovery

**CRITICAL**: Do NOT assume specific tools, package managers, or frameworks.

The ticket's `technical_context.stack` tells you what technologies are involved, but you must **discover** the actual commands from project manifests.

### Finding Project Manifests

```bash
cd {{.WorktreePath}}

# List all project manifests in affected paths
find . -maxdepth 4 \( \
  -name "package.json" -o \
  -name "*.csproj" -o \
  -name "*.sln" -o \
  -name "*.fsproj" -o \
  -name "go.mod" -o \
  -name "go.work" -o \
  -name "Cargo.toml" -o \
  -name "pyproject.toml" -o \
  -name "setup.py" -o \
  -name "requirements.txt" -o \
  -name "Pipfile" -o \
  -name "Gemfile" -o \
  -name "composer.json" -o \
  -name "build.gradle" -o \
  -name "build.gradle.kts" -o \
  -name "pom.xml" -o \
  -name "Makefile" -o \
  -name "justfile" -o \
  -name "Taskfile.yml" -o \
  -name "deno.json" -o \
  -name "bun.lockb" \
\) 2>/dev/null
```

### Discovering Commands by Manifest Type

| Manifest | How to Discover | Build | Test |
|----------|-----------------|-------|------|
| `package.json` | `jq '.scripts' package.json` | `$PM run build` | `$PM run test` |
| `Makefile` | `grep '^[a-z].*:' Makefile` | `make build` | `make test` |
| `justfile` | `just --list` | `just build` | `just test` |
| `Taskfile.yml` | `task --list` | `task build` | `task test` |
| `*.csproj` / `*.sln` | .NET CLI | `dotnet build` | `dotnet test` |
| `go.mod` | Go CLI | `go build ./...` | `go test ./...` |
| `Cargo.toml` | Cargo CLI | `cargo build` | `cargo test` |
| `pyproject.toml` | Check `[build-system]` | varies | `pytest` or configured |
| `deno.json` | `jq '.tasks' deno.json` | varies | `deno test` |

### Package Manager Detection (JavaScript/TypeScript)

```bash
detect_js_pm() {
  if [ -f "pnpm-lock.yaml" ]; then echo "pnpm"
  elif [ -f "yarn.lock" ]; then echo "yarn"
  elif [ -f "bun.lockb" ]; then echo "bun"
  elif [ -f "package-lock.json" ]; then echo "npm"
  elif [ -f "deno.json" ] || [ -f "deno.jsonc" ]; then echo "deno"
  else
    # Check packageManager field in package.json
    PM=$(grep -o '"packageManager":\s*"[^"]*"' package.json 2>/dev/null | cut -d'"' -f4 | cut -d'@' -f1)
    echo "${PM:-npm}"
  fi
}
PM=$(detect_js_pm)
```

### Python Environment Detection

```bash
detect_python_env() {
  if [ -f "poetry.lock" ]; then echo "poetry"
  elif [ -f "Pipfile.lock" ]; then echo "pipenv"
  elif [ -f "uv.lock" ]; then echo "uv"
  elif [ -f "pdm.lock" ]; then echo "pdm"
  elif [ -f "requirements.txt" ]; then echo "pip"
  elif [ -f "pyproject.toml" ]; then
    # Check build system
    if grep -q "poetry" pyproject.toml; then echo "poetry"
    elif grep -q "hatch" pyproject.toml; then echo "hatch"
    elif grep -q "pdm" pyproject.toml; then echo "pdm"
    else echo "pip"
    fi
  else echo "pip"
  fi
}
```

### Running Discovered Commands

After discovering the tooling, use the detected commands:

```bash
# Always check what's available first
if [ -f "package.json" ]; then
  PM=$(detect_js_pm)
  $PM install
  $PM run build  # or whatever script exists
  $PM run test
fi

if [ -f "go.mod" ]; then
  go mod download
  go build ./...
  go test ./...
fi

# etc. for each detected manifest type
```

## Communication Protocol

### Success Output
```json
{
  "status": "passed",
  "agent": "{{.AgentName}}",
  "ticket_id": "{{.Ticket.ID}}",
  "summary": "What was accomplished",
  "artifacts": ["paths to created/modified files"],
  "tooling_used": {
    "manifests": ["package.json", "go.mod"],
    "package_manager": "pnpm",
    "commands_run": ["pnpm build", "go test ./..."]
  },
  "notes": ["Additional context for next agent"]
}
```

### Failure Output
```json
{
  "status": "failed",
  "agent": "{{.AgentName}}",
  "ticket_id": "{{.Ticket.ID}}",
  "reason": "Why it failed",
  "blocking_issues": ["Specific problems"],
  "suggested_actions": ["How to resolve"]
}
```

### Needs-Review Output
```json
{
  "status": "needs-review",
  "agent": "{{.AgentName}}",
  "ticket_id": "{{.Ticket.ID}}",
  "findings": [
    {
      "severity": "critical | high | medium | low",
      "issue": "Description",
      "location": "file:line",
      "recommendation": "How to fix"
    }
  ],
  "decision_required": "What the orchestrator/user needs to decide"
}
```

## Git Commit Conventions

Use conventional commits:
```
<type>(<scope>): <description>

[optional body]

Ticket: {{.Ticket.ID}}
```

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `style`

## Error Handling

When you encounter problems:

1. **Recoverable errors**: Fix and continue
2. **Blocking errors**: Output failure JSON and exit
3. **Uncertainty**: Output needs-review JSON for human decision

**Never:**
- Silently ignore errors
- Make assumptions about unclear requirements
- Commit broken code
- Skip tests to "save time"
- Assume specific tooling without checking manifests first

## Logging

Log your progress to stdout:
```json
{"agent": "{{.AgentName}}", "action": "starting", "ticket": "{{.Ticket.ID}}"}
{"agent": "{{.AgentName}}", "action": "discovered", "manifests": ["package.json", "go.mod"]}
{"agent": "{{.AgentName}}", "action": "detected", "package_manager": "pnpm"}
{"agent": "{{.AgentName}}", "action": "running", "command": "pnpm test"}
{"agent": "{{.AgentName}}", "action": "complete", "duration_ms": 1234}
```
