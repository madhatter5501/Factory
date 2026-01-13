# Contributing to Factory

First off, thank you for considering contributing to Factory! It's people like you that make Factory such a great tool.

## Code of Conduct

This project and everyone participating in it is governed by our commitment to providing a welcoming and inclusive environment. Please be respectful and constructive in your interactions.

## How Can I Contribute?

### Reporting Bugs

Before creating bug reports, please check existing issues to avoid duplicates. When you create a bug report, include as many details as possible:

- **Use a clear and descriptive title**
- **Describe the exact steps to reproduce the problem**
- **Provide specific examples** (code snippets, configuration files)
- **Describe the behavior you observed and what you expected**
- **Include logs and error messages**
- **Specify your environment** (OS, Go version, Factory version)

### Suggesting Enhancements

Enhancement suggestions are tracked as GitHub issues. When creating an enhancement suggestion:

- **Use a clear and descriptive title**
- **Provide a detailed description of the proposed functionality**
- **Explain why this enhancement would be useful**
- **List any alternative solutions you've considered**

### Pull Requests

1. **Fork the repository** and create your branch from `main`
2. **Follow the coding standards** outlined below
3. **Add tests** for any new functionality
4. **Ensure all tests pass** (`make test`)
5. **Run the linter** (`make lint`)
6. **Update documentation** as needed
7. **Write a clear commit message** following conventional commits

## Development Setup

```bash
# Clone your fork
git clone https://github.com/YOUR_USERNAME/Factory.git
cd Factory

# Add upstream remote
git remote add upstream https://github.com/madhatter5501/Factory.git

# Install dependencies
go mod download

# Build
make build

# Run tests
make test

# Run linter
make lint
```

## Coding Standards

### Go Style

- Follow standard Go conventions and idioms
- Use `gofmt` for formatting (run `make fmt`)
- Pass `golangci-lint` checks (run `make lint`)
- Write descriptive variable and function names
- Add comments for exported functions and types

### Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation only
- `style`: Code style (formatting, semicolons, etc.)
- `refactor`: Code change that neither fixes a bug nor adds a feature
- `perf`: Performance improvement
- `test`: Adding or updating tests
- `chore`: Maintenance tasks

Examples:
```
feat(orchestrator): add support for parallel agent execution
fix(kanban): resolve race condition in status transitions
docs(readme): update installation instructions
```

### Testing

- Write unit tests for new functionality
- Ensure tests are deterministic and don't rely on timing
- Use table-driven tests where appropriate
- Mock external dependencies

### Documentation

- Update README.md for user-facing changes
- Add inline comments for complex logic
- Document exported functions and types
- Update examples when APIs change

## Project Structure

```
Factory/
├── cmd/factory/      # CLI entry point - main.go
├── agents/           # Agent spawning and execution
├── git/              # Git worktree management
├── internal/         # Internal packages (not exported)
│   ├── db/           # Database layer
│   └── web/          # Web dashboard
├── kanban/           # Kanban state and types
└── prompts/          # Agent prompt templates
```

### Key Interfaces

When contributing, be aware of these key interfaces:

- `Spawner` - Agent execution interface (CLI/API modes)
- `KanbanStore` - Ticket persistence interface
- `WorktreeManager` - Git worktree operations

## Review Process

1. All PRs require at least one approval
2. CI must pass (lint, test, build)
3. Breaking changes require discussion in an issue first
4. Large changes should be broken into smaller PRs when possible

## Questions?

Feel free to open an issue with the `question` label or reach out to the maintainers.

Thank you for contributing!
