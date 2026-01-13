# Security Policy

Factory takes security seriously. This document outlines our security practices, vulnerability reporting process, and guidelines for contributors.

## Table of Contents

- [Supported Versions](#supported-versions)
- [Reporting a Vulnerability](#reporting-a-vulnerability)
- [Security Architecture](#security-architecture)
- [Development Security Guidelines](#development-security-guidelines)
- [CI/CD Security Checks](#cicd-security-checks)
- [Dependency Management](#dependency-management)
- [Secrets and API Keys](#secrets-and-api-keys)
- [Input Validation](#input-validation)
- [Database Security](#database-security)
- [File Upload Security](#file-upload-security)
- [Web Security](#web-security)
- [Git Worktree Security](#git-worktree-security)
- [Audit Logging](#audit-logging)
- [Security Checklist for Contributors](#security-checklist-for-contributors)

---

## Supported Versions

| Version | Supported          | Notes |
| ------- | ------------------ | ----- |
| 1.x.x   | :white_check_mark: | Current stable release |
| 0.x.x   | :x:                | Pre-release, no security updates |
| < 0.1   | :x:                | Development builds |

We provide security updates for the latest minor version only. Users are encouraged to stay up-to-date with the latest release.

---

## Reporting a Vulnerability

We appreciate responsible disclosure of security vulnerabilities.

### How to Report

1. **DO NOT** open a public GitHub issue for security vulnerabilities
2. Email security concerns to the repository maintainers via GitHub's private vulnerability reporting
3. Or use GitHub's [Security Advisories](https://github.com/madhatter5501/Factory/security/advisories) feature

### What to Include

- Description of the vulnerability
- Steps to reproduce
- Potential impact assessment
- Suggested fix (if available)

### Response Timeline

| Stage | Timeline |
|-------|----------|
| Initial acknowledgment | Within 48 hours |
| Preliminary assessment | Within 7 days |
| Fix development | Within 30 days (critical), 90 days (moderate) |
| Public disclosure | After fix is released |

### Recognition

We maintain a security acknowledgments section for researchers who responsibly disclose vulnerabilities.

---

## Security Architecture

### Trust Boundaries

```
┌─────────────────────────────────────────────────────────────┐
│                    EXTERNAL (Untrusted)                     │
│  • User Input (HTTP requests, form data)                    │
│  • File Uploads                                             │
│  • AI Provider Responses                                    │
└───────────────────────────┬─────────────────────────────────┘
                            │ Validation Layer
┌───────────────────────────▼─────────────────────────────────┐
│                    INTERNAL (Trusted)                       │
│  • Validated configuration                                  │
│  • Internal file paths (prompts/, templates/)               │
│  • Database queries (parameterized)                         │
│  • Git operations (sanitized inputs)                        │
└─────────────────────────────────────────────────────────────┘
```

### Component Security Model

| Component | Security Considerations |
|-----------|------------------------|
| **Web Server** | Input validation, XSS prevention, CSRF tokens |
| **SQLite Database** | Parameterized queries, file permissions |
| **AI Providers** | API key protection, response sanitization |
| **Git Worktrees** | Path traversal prevention, branch name sanitization |
| **File Uploads** | Extension validation, size limits, content type checks |

---

## Development Security Guidelines

### Static Analysis Rules

We enforce security through automated static analysis. The following gosec rules are monitored:

| Rule | Description | Our Approach |
|------|-------------|--------------|
| **G101** | Hardcoded credentials | Environment variables only |
| **G104** | Unhandled errors | Explicit handling or documented ignoring |
| **G107** | URL provided to HTTP request | Validate/sanitize URLs |
| **G201-G202** | SQL injection | Parameterized queries only |
| **G203** | XSS via template.HTML | Sanitize with goldmark, escape user input |
| **G204** | Command injection | Validate command paths at construction |
| **G301** | Insecure file permissions | Use 0750 for directories, 0640 for files |
| **G304** | Path traversal | Validate paths, use filepath.Clean |
| **G401-G501** | Weak crypto | Use crypto/rand, modern algorithms |

### Nosec Comment Format

When suppressing security warnings, use the **standalone gosec format**:

```go
// CORRECT - Works with standalone gosec
// #nosec G304 -- path from internal config, not user input
content, err := os.ReadFile(internalPath)

// INCORRECT - Only works with golangci-lint
content, err := os.ReadFile(internalPath) //nolint:gosec
```

**Rules for using #nosec:**
1. Always include the specific rule code (e.g., `G304`)
2. Always include a justification comment explaining why it's safe
3. Place the comment on the line **before** the flagged code
4. Document in code review why the suppression is necessary

---

## CI/CD Security Checks

Our CI pipeline includes multiple security gates:

### Automated Scans

```yaml
# Security job runs on every PR and push to main
security:
  - govulncheck ./...    # Dependency vulnerability scanning
  - gosec ./...          # Static security analysis
```

### Security Gates

| Check | Tool | Failure Behavior |
|-------|------|------------------|
| Dependency vulnerabilities | `govulncheck` | Blocks merge |
| Static security analysis | `gosec` | Blocks merge |
| Code quality | `golangci-lint` | Blocks merge |
| Race conditions | `go test -race` | Blocks merge |

### Branch Protection

- All PRs require passing security checks
- Direct pushes to `main` are restricted
- Force pushes to `main` are prohibited

---

## Dependency Management

### Principles

1. **Minimal dependencies** - Pure Go implementation where possible
2. **Verified sources** - Only use well-maintained, reputable packages
3. **Regular updates** - Monitor and update dependencies promptly
4. **Lockfile integrity** - Verify `go.sum` checksums

### Current Security-Critical Dependencies

| Package | Purpose | Security Notes |
|---------|---------|----------------|
| `modernc.org/sqlite` | Pure-Go SQLite | No CGO, audited |
| `github.com/google/uuid` | UUID generation | Crypto-safe |
| `github.com/yuin/goldmark` | Markdown rendering | XSS-safe output |
| `anthropic-sdk-go` | Anthropic API | Official SDK |
| `openai-go` | OpenAI API | Official SDK |
| `google.golang.org/genai` | Google AI API | Official SDK |

### Updating Dependencies

```bash
# Check for vulnerabilities
govulncheck ./...

# Update all dependencies
go get -u ./...
go mod tidy

# Verify checksums
go mod verify
```

---

## Secrets and API Keys

### Storage Rules

| Secret Type | Storage Method | Never Store In |
|-------------|---------------|----------------|
| AI API keys | Environment variables | Code, config files, git |
| Database credentials | Environment variables | Logs, error messages |
| Session tokens | Memory only | Persistent storage |

### Environment Variables

```bash
# Required for AI providers
ANTHROPIC_API_KEY=sk-ant-...
OPENAI_API_KEY=sk-...
GOOGLE_API_KEY=...

# Optional configuration
FACTORY_DB_PATH=./factory.db
FACTORY_PORT=8080
```

### Code Patterns

```go
// CORRECT - Read from environment
apiKey := os.Getenv("ANTHROPIC_API_KEY")
if apiKey == "" {
    return errors.New("ANTHROPIC_API_KEY not set")
}

// INCORRECT - Hardcoded key
apiKey := "sk-ant-api03-..." // NEVER DO THIS
```

### .gitignore Requirements

```gitignore
# Secrets
.env
.env.*
*.pem
*.key
credentials.json

# Database files
*.db
*.db-wal
*.db-shm
```

---

## Input Validation

### HTTP Request Validation

All user input must be validated before use:

```go
// Path parameters - validate format
var safeAgentTypeRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func validateAgentType(agentType string) error {
    if !safeAgentTypeRe.MatchString(agentType) {
        return errors.New("invalid agent type")
    }
    return nil
}

// Query parameters - sanitize and bound
func parseLimit(r *http.Request) int {
    limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
    if err != nil || limit < 1 {
        return 10 // default
    }
    if limit > 100 {
        return 100 // maximum
    }
    return limit
}
```

### File Extension Validation

```go
var safeExtensionRe = regexp.MustCompile(`^\.[a-zA-Z0-9]+$`)

func validateFileExtension(filename string) error {
    ext := filepath.Ext(filename)
    if ext != "" && !safeExtensionRe.MatchString(ext) {
        return errors.New("invalid file extension")
    }
    return nil
}
```

### Path Traversal Prevention

```go
// CORRECT - Validate path is within expected directory
func safePath(basePath, userInput string) (string, error) {
    // Clean the path
    cleaned := filepath.Clean(userInput)

    // Ensure no directory traversal
    if strings.Contains(cleaned, "..") {
        return "", errors.New("path traversal detected")
    }

    // Build full path
    fullPath := filepath.Join(basePath, cleaned)

    // Verify it's still within base
    if !strings.HasPrefix(fullPath, basePath) {
        return "", errors.New("path escapes base directory")
    }

    return fullPath, nil
}
```

---

## Database Security

### Parameterized Queries

**Always use parameterized queries** - never concatenate user input into SQL:

```go
// CORRECT - Parameterized query
rows, err := db.QueryContext(ctx,
    "SELECT * FROM tickets WHERE id = ? AND status = ?",
    ticketID, status)

// INCORRECT - SQL injection vulnerability
query := fmt.Sprintf("SELECT * FROM tickets WHERE id = '%s'", ticketID)
rows, err := db.Query(query)
```

### Database File Permissions

```go
// Directory: 0750 (owner: rwx, group: r-x, other: ---)
if err := os.MkdirAll(dir, 0750); err != nil {
    return err
}

// SQLite pragmas for security
db.Exec("PRAGMA journal_mode=WAL")
db.Exec("PRAGMA foreign_keys=ON")
```

### Data Classification

| Data Type | Sensitivity | Retention |
|-----------|-------------|-----------|
| Ticket content | Medium | Persistent |
| Agent prompts | Low | Persistent |
| API responses | Medium | Session only |
| Audit logs | High | 90 days |
| Error messages | Low | 30 days |

---

## File Upload Security

### Validation Checklist

```go
func validateUpload(header *multipart.FileHeader) error {
    // 1. Size limit (10MB)
    if header.Size > 10*1024*1024 {
        return errors.New("file too large")
    }

    // 2. Extension whitelist
    ext := strings.ToLower(filepath.Ext(header.Filename))
    allowed := map[string]bool{
        ".txt": true, ".md": true, ".json": true,
        ".png": true, ".jpg": true, ".pdf": true,
    }
    if !allowed[ext] {
        return errors.New("file type not allowed")
    }

    // 3. Content-Type validation
    contentType := header.Header.Get("Content-Type")
    if !isAllowedContentType(contentType) {
        return errors.New("content type not allowed")
    }

    return nil
}
```

### Storage Security

- Store uploads outside web root
- Use randomly generated filenames (UUIDs)
- Set restrictive file permissions (0640)
- Scan for malware before processing (if applicable)

---

## Web Security

### XSS Prevention

```go
// Template functions with XSS protection
"markdown": func(s string) template.HTML {
    var buf bytes.Buffer
    if err := goldmark.Convert([]byte(s), &buf); err != nil {
        // ALWAYS escape on error
        return template.HTML(template.HTMLEscapeString(s))
    }
    // goldmark produces safe HTML
    return template.HTML(buf.String())
},
```

### Content Security Policy

Recommended headers for the web dashboard:

```go
func securityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("X-XSS-Protection", "1; mode=block")
        w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
        w.Header().Set("Content-Security-Policy",
            "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
        next.ServeHTTP(w, r)
    })
}
```

### CORS Configuration

The web server should only allow same-origin requests unless explicitly configured:

```go
// Default: No CORS headers (same-origin only)
// If CORS needed, whitelist specific origins
```

---

## Git Worktree Security

### Branch Name Sanitization

```go
func sanitizeBranchName(branch string) string {
    // Remove common prefixes
    branch = strings.TrimPrefix(branch, "feat/")
    branch = strings.TrimPrefix(branch, "fix/")

    // Replace unsafe characters
    re := regexp.MustCompile(`[^a-zA-Z0-9-_]`)
    return re.ReplaceAllString(branch, "-")
}
```

### Command Execution

```go
// CORRECT - Path validated at construction time
type Spawner struct {
    claudePath string // validated in NewSpawner()
}

func NewSpawner(claudePath string) (*Spawner, error) {
    // Validate the path exists and is executable
    info, err := os.Stat(claudePath)
    if err != nil {
        return nil, fmt.Errorf("claude not found: %w", err)
    }
    if info.Mode()&0111 == 0 {
        return nil, errors.New("claude is not executable")
    }
    return &Spawner{claudePath: claudePath}, nil
}

func (s *Spawner) Run(ctx context.Context, prompt string) error {
    // #nosec G204 -- claudePath is validated at construction time
    cmd := exec.CommandContext(ctx, s.claudePath, "--print")
    // ...
}
```

---

## Audit Logging

### What We Log

| Event | Data Logged | Sensitivity |
|-------|-------------|-------------|
| Agent invocations | Agent type, ticket ID, duration | Medium |
| API requests | Endpoint, method, response code | Low |
| Authentication | User ID, timestamp, result | High |
| Errors | Error type, stack trace (no secrets) | Medium |
| File operations | Filename, operation, result | Low |

### What We Never Log

- API keys or tokens
- Full request/response bodies with sensitive data
- User passwords or credentials
- PII beyond what's necessary for debugging

### Log Format

```go
s.logger.Info("Agent completed",
    "run_id", runID,
    "ticket_id", ticketID,
    "agent_type", agentType,
    "duration_ms", duration,
    "success", result.Success,
)
```

---

## Security Checklist for Contributors

Before submitting a PR, verify:

### Code Security

- [ ] No hardcoded secrets, API keys, or credentials
- [ ] All SQL queries use parameterized statements
- [ ] User input is validated before use
- [ ] File paths are sanitized and validated
- [ ] Error messages don't leak sensitive information
- [ ] New dependencies are justified and from trusted sources

### Testing

- [ ] Security-sensitive code has test coverage
- [ ] Tests don't use real credentials
- [ ] Race conditions are checked (`go test -race`)

### Documentation

- [ ] Security implications of changes are documented
- [ ] Any new #nosec comments include justification
- [ ] API changes include input validation requirements

### CI Checks

- [ ] `gosec ./...` passes with no new issues
- [ ] `govulncheck ./...` shows no vulnerabilities
- [ ] `golangci-lint` passes

---

## Security Contacts

- **Security Issues**: Use GitHub's private vulnerability reporting
- **General Questions**: Open a GitHub Discussion
- **Urgent Issues**: Contact repository maintainers directly

---

## Acknowledgments

We thank the following security researchers for responsibly disclosing vulnerabilities:

*No vulnerabilities reported yet.*

---

*Last updated: January 2026*
