---
name: upstream-fix
description: "Runbook and procedure for developing, verifying, formatting, and submitting bug fixes or patches destined for upstream (Mag1cFall/AIStudio2API:main) without leaking fork refactors, plus backporting them to dev."
---

# Upstream Bug Fix Runbook (`upstream-fix`)

This skill defines the complete operational procedure for identifying, fixing, testing, and submitting bug fixes or patches intended for the upstream repository (`Mag1cFall/AIStudio2API:main`), while ensuring fork-specific modifications are never leaked into upstream PRs.

---

## 1. The Core Principle: Strict Fork Isolation

Our fork's `dev` branch contains significant, intentional divergence from upstream:
- Full translation of comments, error messages, and logs to English.
- Strictly immutable runtime configuration (Docker-safe, no runtime `.env` writes, no `PUT /api/config`).
- Removal of legacy scripts and custom account leasing / architecture adjustments.

### The Invariant
**NEVER develop an upstream fix on the `dev` branch.**
- If you branch from `dev`, the git diff will include hundreds or thousands of unrelated fork changes. Upstream maintainers will reject the pull request.
- All upstream fixes **must be branched directly from `upstream-main`** (which mirrors `upstream/main`).
- The diff must be surgical, minimal, and focused strictly on the bug.

---

## 2. Step-by-Step Upstream Fix Procedure

### Step 1: Update Upstream Tracking & Create Fix Branch

```bash
# 1. Fetch latest commits from upstream
git fetch upstream

# 2. Fast-forward local upstream-main to upstream/main
git branch -f upstream-main upstream/main

# 3. Create a dedicated fix branch branched directly from upstream-main
git checkout upstream-main
git checkout -b fix/<issue-slug>

# Example:
# git checkout -b fix/benefit-tier-response-parsing
```

### Step 2: Reproduce the Bug & Write a Failing Test

Before applying a fix, isolate and reproduce the failure:
1. Identify the affected component (e.g., `internal/aistudio/`, `internal/camoufoxnative/`, `internal/api/`).
2. Write a minimal unit test in the relevant `*_test.go` file asserting the expected behavior:
   - For wire protocol issues: mock the raw upstream array response and test deserialization.
   - For Camoufox/BiDi issues: test attribute normalization or error classification.
   - For API adapters: test payload transformation and error propagation.
3. Run the test to confirm it fails on the current upstream code:
   ```bash
   go test -v ./internal/aistudio -run TestSpecificBug
   ```

### Step 3: Implement the Surgical Fix

Adhere strictly to upstream development conventions:

1. **Minimal Blast Radius**: Touch only the exact lines required to fix the root cause.
2. **No Gratuitous Refactoring**:
   - Do not reformat untouched functions or files.
   - Do not re-organize imports unless required for new packages.
3. **Language & Style Conformance**:
   - Do **not** translate existing Chinese comments in untouched code to English on this branch.
   - New code comments and error messages should be written in clear, concise technical English (or bilingual if requested).
   - Follow existing Go patterns in the upstream codebase.
4. **MakerSuite Wire Protocol Array Invariants**:
   - Upstream uses raw JSON arrays where field $N$ in Google's internal protobuf maps to index $N - 1$.
   - When inspecting or unpacking arrays, guard against out-of-bounds indices and unexpected nil elements (e.g. `len(arr) > index && arr[index] != nil`).

### Step 4: Verify on the Upstream Branch

Run the complete upstream verification suite:

```bash
# Run static analysis
go vet ./...

# Run all unit tests
go test ./...

# Verify clean compilation
go build ./cmd/aistudio2api
```

### Step 5: Commit Following Upstream Conventions

Write a concise, professional commit message following Conventional Commits:

```text
fix(<subsystem>): <concise summary of the bug fix>

<Explain the problem: what caused the bug or unexpected behavior>
<Explain the solution: how the change resolves it safely>
```

**Real Examples from Upstream History:**
```text
fix(aistudio): make GetAiStudioBenefitTier response parsing resilient

Prevent service startup failures when AI Studio returns empty or unexpected
root array lengths in GetAiStudioBenefitTier RPC. Defaults to BenefitTierFree
instead of failing model synchronization with ProtocolEvidenceError.
```
```text
fix(camoufoxnative): normalize non-standard SameSite cookie attributes during login

Handle cases where Google returns lowercase or custom SameSite attributes
that cause WebDriver BiDi cookie deserialization to fail.
```

### Step 6: Push & Submit Upstream Pull Request

1. **Push to Fork Remote (`origin`)**:
   ```bash
   git push -u origin fix/<issue-slug>
   ```

2. **Inspect the Clean Diff**:
   Ensure no fork changes leaked into the branch:
   ```bash
   git diff upstream-main...HEAD
   ```
   *Only the lines directly resolving the bug must appear in the diff.*

3. **Open Pull Request to `Mag1cFall/AIStudio2API`**:
   ```bash
   gh pr create \
     --repo Mag1cFall/AIStudio2API \
     --base main \
     --head IllusionOfControl:fix/<issue-slug> \
     --title "fix(<subsystem>): <description>" \
     --body "<Description of bug and fix>"
   ```

   *(Alternatively, export a patch via `git format-patch upstream-main...HEAD -o patches/`).*

---

## 3. Backporting / Cherry-Picking the Fix into `dev`

After the fix is committed on `fix/<issue-slug>` and ready for upstream, backport it to our fork:

### Step 1: Cherry-Pick onto `dev`

```bash
# 1. Switch to dev and create a local integration branch
git checkout dev
git checkout -b fix/<issue-slug>-dev

# 2. Cherry-pick the fix commit
git cherry-pick <commit-hash>
```

### Step 2: Apply Fork Invariants

During or immediately after the cherry-pick:
1. **Translate**: If any new comments, log messages, or error strings were added, ensure they comply with the English-only invariant.
2. **Immutable Config**: Ensure the fix does not introduce runtime `.env` writes or violate Docker read-only constraints.
3. **Verify**:
   ```bash
   go vet ./...
   go test ./...
   ```

### Step 3: Document and Merge into `dev`

1. Create a branch document `docs/branches/fix-<issue-slug>.md` using `_template.md`.
2. Register the branch in `docs/branches/_index.md`.
3. Merge into `dev`:
   ```bash
   git checkout dev
   git merge --no-ff fix/<issue-slug>-dev -m "Merge branch 'fix/<issue-slug>-dev' into dev"
   ```
4. Log the merge in `docs/branches/_dev.md`.

---

## 4. Pre-Flight Checklist for Upstream Fixes

- [ ] Branch was created from `upstream-main` / `upstream/main` (NOT from `dev`).
- [ ] `git diff upstream-main` shows **only** the lines required for the bug fix.
- [ ] No fork-specific refactors (mass translations, Docker changes, readonly config changes) are in the diff.
- [ ] `go test ./...` and `go vet ./...` pass on the upstream branch.
- [ ] PR description clearly explains the root cause and why the fix is safe.
- [ ] Fix has been cherry-picked to `dev` with fork invariants (English comments, read-only config) applied.
