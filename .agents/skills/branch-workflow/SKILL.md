---
name: branch-workflow
description: "Runbook and automated procedure for creating, implementing, verifying, and documenting new feature, fix, or refactor branches off dev according to repository invariants and documentation standards."
---

# Feature, Fix & Refactor Branch Workflow (`branch-workflow`)

This skill defines the mandatory repository lifecycle for creating and developing any new `feature/*`, `fix/*`, `refactor/*`, or `docs/*` branch in the `AIStudio2API` repository.

---

## 1. Branch Naming & Classification

All development branches (except upstream sync branches) must branch from `dev` and merge back into `dev`:

| Branch Prefix | Usage | Examples |
|---|---|---|
| `feature/<slug>` | New capabilities, APIs, CLI commands, UI panels | `feature/terminal-login`, `feature/docker-compose`, `feature/waa-auto-start` |
| `fix/<slug>` | Fork-specific bug fixes, edge cases, protocol normalization | `fix/camoufox-cookie-samesite-normalization`, `fix/json-schema-normalization` |
| `refactor/<slug>` | Code reorganization, translation, architecture cleanups | `refactor/readonly-config`, `refactor/remove-start-bat`, `refactor/translate-to-english` |
| `docs/<slug>` | Substantial documentation updates, specifications | `docs/agents-documentation`, `docs/translate-to-english` |

---

## 2. Step-by-Step Lifecycle

### Phase 1: Branch Initialization

1. **Verify Workspace Cleanliness**:
   Ensure `git status` has no uncommitted work from previous tasks.
2. **Update `dev`**:
   ```bash
   git checkout dev
   git pull --ff-only
   ```
3. **Create the New Branch**:
   ```bash
   git checkout -b <type>/<slug>
   # Example: git checkout -b feature/prometheus-metrics
   ```

### Phase 2: Mandatory Branch Documentation Setup

Every new branch **must** be registered and tracked in `docs/branches/`:

1. **Create Branch Document**:
   Copy `docs/branches/_template.md` to `docs/branches/<type>-<slug>.md`:
   ```yaml
   ---
   branch: <type>/<slug>
   last_commit: pending
   status: in-process
   last_activity: DD-MM-YYYY
   ---

   ## Task

   <Concise description of the objective>

   ## Description

   <Detailed background, rationale, and problem statement>

   ## Changes

   <Bullet points of planned or completed file changes>

   ## Result

   <Verification results and status>
   ```

2. **Register in `docs/branches/_index.md`**:
   Add a new row to the `## Summary Table` in `docs/branches/_index.md`:
   ```markdown
   | [`<type>/<slug>`](<type>-<slug>.md) | <Purpose / Scope> | <Date> | pending | in-process | [<type>-<slug>.md](<type>-<slug>.md) |
   ```

---

## 3. Implementation Invariants (Non-Negotiable)

When writing or modifying code in this repository, strictly enforce these core invariants:

### A. English-Only Code & Logs
- All Go comments, documentation, error strings, log messages, and CLI outputs **must be in English**.
- Chinese is permitted **only** in user-facing translation files (`web/src/i18n.ts`) and localized documentation (`README.md`, `docs/`).

### B. Immutable Runtime Configuration (Docker-Safe)
- Configuration is **strictly read-only at runtime** (refactor `6e318e0`).
- The application loads settings once upon startup from environment variables and `.env`.
- **Never**:
  - Add `PUT /api/config` or runtime reloading endpoints.
  - Write methods that modify `.env` or configuration files on disk at runtime.
  - Add mutable form fields or "Save" buttons to the Web UI Settings panel (`SettingsPanel.vue`). The Settings panel must remain a read-only inspector.

### C. Architecture Layer Separation
- `internal/api/`: External protocol serialization/deserialization only (OpenAI, Anthropic, Gemini, Responses, Admin HTTP). Never touch Camoufox processes, disk locks, or low-level account leases directly.
- `internal/app/`: Lifecycle coordination, service start/restart, worker pool management, account leasing, and scheduling.
- `internal/aistudio/`: Google MakerSuite wire protocol encoding/decoding (`field N -> index N-1`), RPC clients, and account pool structures.
- `internal/waa/`: Pure-Go BotGuard VM runtime, Firefox DOM simulation, Goja JavaScript VM.
- `internal/camoufoxnative/`: Camoufox browser solver, BiDi protocol client, and profile isolation.
- `cmd/aistudio2api/`: Thin entry point delegating to `app.Run()`.

### D. Chrome Account Format
- Accounts are identified by `ID: "<Profile>/<GaiaID>"`.
- API and CLI use `AccountIDs []string` / `account_ids`, not profile names.

---

## 4. Frontend Build & Asset Embedding (if `web/` touched)

If any changes are made to the Web UI in `web/`:

```bash
cd web

# 1. Validate TypeScript types
npm run typecheck

# 2. Build production assets into internal/webui/dist/
npm run build

# 3. Verify linting and formatting
npm run lint
npm run format:check

cd ..
```

*Note: `internal/webui/embed.go` embeds `internal/webui/dist/` at compile time. Never commit frontend code without rebuilding `dist/`.*

---

## 5. Verification Suite

Run full verification before committing:

```bash
# 1. Static analysis
go vet ./...

# 2. Unit and integration tests
go test ./...

# 3. Compilation check
go build ./cmd/aistudio2api
```

Both `go vet` and `go test` must pass with 0 errors.

---

## 6. Commit & Branch Finalization

1. **Commit Code Changes**:
   Follow Conventional Commits: `<type>(<scope>): <summary>`:
   ```bash
   git add <modified-files>
   git commit -m "feat(scope): add new feature capability"
   ```
2. **Update Branch Documentation**:
   - In `docs/branches/<type>-<slug>.md`:
     - Update `last_commit: <commit-hash>`.
     - Update `status: ready` (or `completed` if merging immediately).
     - Fill in `## Changes` and `## Result` with actual diff details and test evidence.
   - In `docs/branches/_index.md`:
     - Update the commit column with `<commit-hash>` and status to `Ready`.
3. **Commit Documentation Record**:
   ```bash
   git commit -am "docs(branches): record commit hash in branch index"
   ```

---

## 7. Merge to `dev` & Change Logging (When Instructed / Ready)

When merging the branch into `dev`:

1. **Merge Branch**:
   ```bash
   git checkout dev
   git merge --no-ff <type>/<slug> -m "Merge branch '<type>/<slug>' into dev"
   ```
2. **Log Merge in `docs/branches/_dev.md`**:
   - Add a new row to `## Commit Summary Table`:
     ```markdown
     | **YYYY-MM-DD** | `<commit-hash>` | Sergey Skorokhod | `<type>/<slug>` | [`<type>-<slug>.md`](<type>-<slug>.md) | <Concise summary of changes> |
     ```
   - Append a detailed subsection under the table describing the changes.
3. **Update Status in `docs/branches/_index.md`**:
   - Change branch status from `Ready` to `Merged into dev`.
4. **Commit the Dev Merge Record**:
   ```bash
   git commit -am "docs(branches): log merge of <type>/<slug> into dev"
   ```

---

## 8. Branch Workflow Pre-Flight Checklist

- [ ] Branch name adheres to `<type>/<slug>` (`feature/`, `fix/`, `refactor/`, `docs/`).
- [ ] Document `docs/branches/<type>-<slug>.md` exists and is registered in `docs/branches/_index.md`.
- [ ] All new comments, logs, and error strings are in English.
- [ ] No mutable runtime config or `.env` writes were introduced.
- [ ] If `web/` changed, `npm run build` completed and `internal/webui/dist/` is committed.
- [ ] `go vet ./...` and `go test ./...` pass with 0 errors.
- [ ] Conventional Commit message used.
- [ ] If merging into `dev`, entry added to `docs/branches/_dev.md`.
