---
name: sync-upstream
description: "Runbook and automated procedure for synchronizing updates from upstream-main (Mag1cFall/AIStudio2API) into dev while strictly preserving repository invariants (English-only code and logs, read-only Docker config, and account ID format)."
---

# Upstream Synchronization Runbook (`sync-upstream`)

This skill defines the complete operational procedure for pulling, resolving, adapting, and merging upstream updates from `Mag1cFall/AIStudio2API:main` into this repository's `dev` branch.

---

## 1. Architectural Invariants (Non-Negotiable)

When merging changes from upstream, you **must never** compromise these repository invariants:

### A. English-Only Code, Comments, and Logs
- Upstream commits from `Mag1cFall/AIStudio2API` frequently include Chinese comments, log messages, CLI output, and error strings.
- **Rule**: Every single comment, log line, error message, CLI output, and struct field description in Go (`*.go`) and TypeScript (`web/src/**/*.ts`) **must be translated into concise, natural English**.
- **Exception**: User-facing i18n translation files (`web/src/i18n.ts`) and localized documentation (`README.md`, `docs/`).

### B. Immutable Runtime Configuration (Docker-Safe)
- Upstream frequently attempts to re-introduce runtime configuration mutation:
  - `PUT /api/config` or configuration reload endpoints in `internal/api/admin.go`.
  - `.env` file writing methods (`config.Save()`, `atomicWrite()`, mutex locks for file writing) in `internal/config/config.go`.
  - UI editable forms and Save buttons in `web/src/components/SettingsPanel.vue`.
- **Rule**: Configuration is **strictly read-only at runtime** (established in refactor `6e318e0`).
  - Read all settings from environment variables once upon startup.
  - If upstream adds new configuration options (e.g. `UPSTREAM_CHANNELS`, `WAA_BACKEND`, `ADMIN_AUTH`), declare them in `internal/config/config.go` with default values and environment variable bindings.
  - The Web UI Settings panel (`SettingsPanel.vue`) must only act as a **read-only configuration inspector**.
  - Reject/drop all upstream code that writes to `.env` or mutates disk configuration during application runtime.

### C. Chrome Import by Account ID
- Upstream supports multiple distinct Google accounts from a single Chrome profile.
- Accounts are identified by `ID: "<Profile>/<GaiaID>"`.
- API and CLI use `AccountIDs []string` / `account_ids`, not profile names.

### D. Single-Direction Flow & Layer Separation
- `internal/api/`: Public protocol adapters (OpenAI, Anthropic, Gemini, Responses, Admin HTTP). No direct Camoufox process management or low-level account leases.
- `internal/app/`: Lifecycle orchestration, service restart, account leasing, worker pooling, and scheduling.
- `internal/aistudio/`: Google MakerSuite wire protocol encoding/decoding and account candidate structures.
- `internal/waa/`: Pure-Go BotGuard VM runtime, Firefox DOM simulation, Goja JavaScript VM.
- `internal/camoufoxnative/`: Camoufox browser solver, BiDi protocol client, and profile isolation.

### E. Frontend Build & Binary Embedding
- `internal/webui/embed.go` embeds `internal/webui/dist/` into the Go binary at compile time.
- If upstream modified any files in `web/`, the frontend bundle must be validated (`npm run typecheck`) and rebuilt (`npm run build`) before running Go tests or committing.

---

## 2. Step-by-Step Synchronization Procedure

### Step 1: Fetch Upstream & Prepare Branch

```bash
# 1. Fetch latest upstream commits
git fetch upstream

# 2. Fast-forward upstream-main to mirror upstream/main
git branch -f upstream-main upstream/main

# 3. Ensure dev is clean and checked out
git checkout dev
git pull --ff-only

# 4. Create a dedicated sync branch (e.g., sync/upstream-YYYYMMDD or sync/upstream-vX.Y.Z)
git checkout -b sync/upstream-$(date +%Y%m%d)
```

### Step 2: Merge `upstream-main`

```bash
git merge upstream-main
```

### Step 3: Resolve Merge Conflicts

When merge conflicts occur, use these decision matrices:

| Conflicted File | Resolution Policy |
|---|---|
| `internal/config/config.go` | Accept new configuration keys/struct fields. **Discard** `Save()`, `atomicWrite()`, and `.env` mutation logic. Keep config struct read-only. |
| `internal/api/admin.go` | Keep `GET /api/config` read-only. **Discard** `PUT /api/config` or dynamic update handlers. |
| `internal/app/admin.go` | Keep read-only config inspection. Discard runtime reload or config file overwriting. |
| `web/src/components/SettingsPanel.vue` | Display new configuration fields as read-only informative tags/chips. Do **not** add form inputs with "Save" buttons. |
| `internal/aistudio/generate.go` | Check array slot indexing rules. Upstream MakerSuite wire protocol uses array indexing where field $N$ maps to index $N - 1$. |
| `internal/aistudio/models.go` | Accept new model definitions and capability flags; ensure all enum comments and error messages are in English. |
| `go.mod` / `go.sum` | Accept upstream dependency additions; run `go mod tidy` afterwards. |

### Step 4: Scan and Translate Chinese Text

Run a regex audit on modified files to detect Chinese characters:

```powershell
# Git diff check for Chinese characters in Go files (PowerShell)
git diff dev...HEAD --name-only | Where-Object { $_ -like "internal/*.go" -or $_ -like "cmd/*.go" } | ForEach-Object {
    $content = Get-Content $_ -Raw
    if ($content -match "[\u4e00-\u9fa5]") {
        Write-Host "Found Chinese characters in: $_"
    }
}
```

```bash
# Bash equivalent using PCRE grep:
git diff dev...HEAD --name-only | grep -E '\.(go|ts)$' | grep -v 'web/src/i18n.ts' | while read -r file; do
    if grep -P -q '[\x{4e00}-\x{9fa5}]' "$file"; then
        echo "Found Chinese in: $file"
    fi
done
```

Translate all discovered comments, log messages, panic strings, and errors into concise, idiomatic technical English.

### Step 5: Frontend Verification & Build (if `web/` touched)

If any files under `web/` were modified by upstream or during conflict resolution:

```bash
cd web
npm run typecheck
npm run build
npm run lint
npm run format:check
cd ..
```

*Ensure `internal/webui/dist/` is updated and committed.*

### Step 6: Backend Verification & Static Analysis

```bash
# Static analysis
go vet ./...

# Unit and integration test suites
go test ./...

# Compilation verification
go build ./cmd/aistudio2api
```

All tests and builds must succeed with 0 errors and 0 warnings.

### Step 7: Update Documentation & Logs

1. **Update `docs/branches/sync-upstream-main.md`**:
   - Append a new section header: `### Update: Upstream <Version/Feature> Synchronization (<Date>)`.
   - List the upstream commit range, major upstream features, conflict resolution decisions, and verification results.
2. **Commit the Sync Branch**:
   ```bash
   git commit -m "sync: upstream changes up to <commit-hash> (translated to English, read-only config preserved)"
   ```
3. **Merge into `dev`**:
   ```bash
   git checkout dev
   git merge --no-ff sync/upstream-YYYYMMDD -m "Merge branch 'sync/upstream-YYYYMMDD' into dev"
   ```
4. **Log Merge in `docs/branches/_dev.md`**:
   - Add a new changelog entry with Date, Commit, Author, Source Branch, and bulleted summary.
5. **Update `docs/branches/_index.md`**:
   - Update active date, commit hash, and status.

---

## 3. Quick Checklist Before Merging to `dev`

- [ ] All new comments, logs, and error strings are in English (0 Chinese characters in Go/TS source).
- [ ] Runtime configuration remains strictly immutable (`.env` is never written by the application).
- [ ] `AccountIDs []string` / `<Profile>/<GaiaID>` format is preserved.
- [ ] If `web/` changed, `npm run build` completed and `internal/webui/dist/` is committed.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` reports 0 issues.
- [ ] `docs/branches/sync-upstream-main.md` is updated.
- [ ] `docs/branches/_dev.md` contains the merge record.
