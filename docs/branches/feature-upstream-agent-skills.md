---
branch: feature/upstream-agent-skills
last_commit: f35155c
status: ready
last_activity: 04-10-2026
---

## Task

Create a comprehensive suite of agent skills (`.agents/skills/sync-upstream/` and `.agents/skills/upstream-fix/`) defining standard, repeatable runbooks and automated guardrails for interacting with upstream `Mag1cFall/AIStudio2API:main`:
1. Synchronizing updates into `dev` while preserving fork invariants (English codebase, immutable config, account format).
2. Authoring, testing, and submitting bug fixes intended for upstream pull requests without leaking fork-specific refactors.

## Description

Working with upstream involves two distinct, high-stakes directions with inverse requirements:
1. **Ingress (Upstream $\to$ Fork `dev`)**:
   - Requires enforcing fork invariants: translating upstream Chinese text into English, rejecting mutable runtime config (`PUT /api/config`, `.env` file writing), ensuring Chrome account format (`<Profile>/<GaiaID>`), and compiling frontend embedded assets.
2. **Egress (Bug Fix $\to$ Upstream PR)**:
   - Requires strict fork isolation: branching directly from `upstream-main` (or `upstream/main`), never from `dev`, to prevent leaking thousands of fork-specific refactors into upstream PRs.
   - Surgical blast radius, preserving upstream code style, and dual-porting the fix back to `dev` via `git cherry-pick`.

This branch provides dedicated Agent Skills for both workflows, conforming to cross-agent standards (omp, Claude Code, Cursor/Codex).

## Changes

1. **Upstream Sync Skill (`.agents/skills/sync-upstream/SKILL.md`)**:
   - Standardized fast-forward, sync branch creation, and merge procedure.
   - Conflict resolution decision matrix for config, admin endpoints, WebUI, and wire array slots.
   - Automated PowerShell/bash regex scripts to audit Go and TypeScript files for Chinese characters (`[\p{Han}]`).
   - Pre-merge verification gates (`npm run build`, `go vet`, `go test`) and documentation rules (`sync-upstream-main.md`, `_dev.md`).
2. **Upstream Fix Skill (`.agents/skills/upstream-fix/SKILL.md`)**:
   - Defined core invariant of fork isolation: always branch fixes from `upstream-main`, never `dev`.
   - Step-by-step lifecycle: bug reproduction, minimal fix, verification, Conventional Commits, and PR submission (`gh pr create` / `git format-patch`).
   - Backport protocol: cherry-picking into `dev` with fork invariant enforcement.
3. **Documentation & Cross-References**:
   - `AGENTS.md`: Added references to both skills under Git Branching Strategy.
   - `docs/agents/workflows.md`: Added references to both skills in Upstream Synchronization Workflow.

## Result

- Both Agent Skills deployed under `.agents/skills/`.
- Clean separation of upstream ingress (sync) and egress (bug fixes).
- Repository documentation updated and aligned.
