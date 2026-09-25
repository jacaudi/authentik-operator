# authentik-operator - Claude Code Configuration

## Context

Kubernetes operator (Kubebuilder) for deploying and managing authentik. See `AGENTS.md`
for the Kubebuilder project layout, generated-file rules, and make targets.

**Tech Stack**: Go 1.26, Kubebuilder, controller-runtime v0.25, client-go v0.37, Ginkgo/Gomega (envtest)

**Core Principle**: Declarative, idempotent reconciliation; adoption of existing resources is explicit and opt-in.

**Last Updated**: 2026-09-24
**Navigator Version**: 7.7.1

---
## Navigator Quick Start

**Every session begins with**:
```
"Start my Navigator session"
```

This loads `.agent/DEVELOPMENT-README.md` (your project navigator) which provides:
- Documentation index and "when to read what" guide
- Current task context from PM tool (if configured)
- Quick start guides and integration status

**Core workflow**:
1. **Start session** → Loads navigator automatically
2. **Load task docs** → Only what's needed for current work
3. **Implement** → Follow project patterns below
4. **Document** → "Archive TASK-XX documentation" when complete
5. **Compact** → "Clear context and preserve markers" after isolated tasks

**Natural language commands**:
- "Start my Navigator session" (begin work)
- "Archive TASK-XX documentation" (after completion)
- "Create an SOP for debugging [issue]" (document solution)
- "Clear context and preserve markers" (after sub-tasks)

**For full Navigator workflow**: See plugin's root CLAUDE.md or `.agent/DEVELOPMENT-README.md`

---

## Project-Specific Code Standards

- Follow `AGENTS.md` for layout; never hand-edit generated files (`zz_generated.*`,
  `config/crd/bases/*`, `config/rbac/role.yaml`, `PROJECT`) -- run `make manifests generate`.
- Tests via `go test` / `make test` (envtest); run `go vet` and `make lint` (golangci-lint).
- Errors as values, wrapped with context; reconcilers must be idempotent.
- No emoji in code, strings, or scripts -- ASCII only.
- If it runs in a container, test it in a container; rebuild images to deliver changes.
- Kubernetes YAML: one object per file (`<kind>-<name>.yaml`).

---

## Forbidden Actions

### Navigator Violations (HIGHEST PRIORITY)
- NEVER leave tickets open after completion
- NEVER skip documentation after features
- NEVER load all `.agent/` docs at once (defeats token optimization)
- NEVER skip reading DEVELOPMENT-README.md navigator

### General Violations
- Never commit secrets/API keys/.env files (use 1Password/Vault and sync)
- Don't delete tests without replacement
- Don't edit generated files by hand

---
## Documentation Structure

```
.agent/
├── DEVELOPMENT-README.md      # Navigator (always load first)
├── tasks/                     # Implementation plans
├── system/                    # Architecture docs
└── sops/                      # Standard Operating Procedures
    ├── integrations/
    ├── debugging/
    ├── development/
    └── deployment/
```

**Token-efficient loading**:
- Navigator: ~2k tokens (always)
- Current task: ~3k tokens (as needed)
- System docs: ~5k tokens (when relevant)
- SOPs: ~2k tokens (if required)
- **Total**: ~12k vs ~150k loading everything

---

## Project Management Integration

[Configure based on your setup - remove this section if not using PM integration]

**Configured Tool**: [Linear / GitHub Issues / Jira / GitLab / None]

**Workflow**:
1. Read ticket via PM tool
2. Generate implementation plan → `.agent/tasks/`
3. Implement features
4. Update system docs if architecture changes
5. Complete → "Archive TASK-XX documentation" (auto-closes ticket)
6. Notify team (if chat integration configured)

---

## Configuration

Navigator config in `.agent/.nav-config.json`:

```json
{
  "version": "7.7.1",
  "project_management": "none",
  "task_prefix": "TASK",
  "team_chat": "none",
  "auto_load_navigator": true,
  "compact_strategy": "conservative",
  "auto_update": {
    "enabled": true,
    "check_interval_hours": 1
  },
  "session_start_hook": {
    "enabled": true,
    "include_sections": ["navigator", "marker", "config", "graph", "profile", "tasks", "auto_update"],
    "char_budget": 9500
  },
  "compact_hook": {
    "enabled": true,
    "include_transcript_summary": true,
    "include_git_state": true,
    "char_budget": 8000,
    "append_post_compact_summary": true
  },
  "task_graph_sync_hook": { "enabled": true },
  "workflow_state_hook":  { "enabled": true },
  "profile_sync_hook":    { "enabled": true }
}
```

**Customize after initialization**:
- `project_management`: "linear" | "github" | "jira" | "gitlab" | "none"
- `task_prefix`: Your ticket prefix (e.g., "PROJ", "DEV")
- `team_chat`: "slack" | "discord" | "none"

---

## Commit Guidelines

- **Format**: `type(scope): description`
- **Reference ticket**: `feat(auth): implement OAuth login TASK-XX`
- **Types**: feat, fix, docs, refactor, test, chore
- Concise and descriptive

---

## Success Metrics

### Context Efficiency
- <70% token usage for typical tasks
- <12k tokens loaded per session
- 10+ exchanges without compact
- Zero session restarts during features

### Documentation Coverage
- 100% completed features have task docs
- 90%+ integrations have SOPs
- System docs updated within 24h
- Zero repeated mistakes

---

**For complete Navigator documentation**:
- `.agent/DEVELOPMENT-README.md` (project navigator)
- Plugin's root CLAUDE.md (full workflow reference)
