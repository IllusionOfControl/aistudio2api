---
branch: feature/upstream-agent-skills
last_commit: f35155c
status: ready
last_activity: 04-10-2026
---

## Task

Create a comprehensive suite of agent skills (`.agents/skills/`) defining standard, repeatable runbooks and automated guardrails for development, upstream synchronization, and bug fixing:
1. **Branch Workflow (`branch-workflow`)**: Complete lifecycle for creating, implementing, documenting, and testing any `feature/*`, `fix/*`, or `refactor/*` branch.
2. **Upstream Sync (`sync-upstream`)**: Synchronizing updates from `upstream-main` into `dev` while strictly preserving fork invariants (English codebase, immutable config, account format).
3. **Upstream Fix (`upstream-fix`)**: Authoring, testing, and submitting bug fixes intended for upstream pull requests without leaking fork-specific refactors.

## Description

Autonomous coding agents and contributors need precise, procedural runbooks that enforce repository invariants:
- **Feature/Fix/Refactor Development**: Enforces consistent branch naming, mandatory documentation in `docs/branches/`, strict architectural layer separation, English-only code/logs, and immutable runtime config.
- **Upstream Ingress (`sync-upstream`)**: Pulling upstream updates safely, auditing for Chinese text, rejecting mutable configuration endpoints, and rebuilding frontend assets.
- **Upstream Egress (`upstream-fix`)**: Developing bug fixes isolated from fork refactors on `upstream-main` with minimal blast radius, preparing upstream PRs, and backporting to `dev`.

These skills conform to cross-agent standards (omp, Claude Code, Cursor/Codex) and load on demand.

## Changes

1. **Branch Workflow Skill (`.agents/skills/branch-workflow/SKILL.md`)**:
   - Defined branch naming conventions (`feature/`, `fix/`, `refactor/`, `docs/`).
   - Step-by-step lifecycle from `dev` checkout to branch documentation setup (`docs/branches/`).
   - Enforcement of architectural invariants (English-only code, Docker-safe immutable config, layer separation).
   - Verification gates (`go vet`, `go test`, `npm run build`), Conventional Commits, and `_dev.md` logging.
2. **Upstream Sync Skill (`.agents/skills/sync-upstream/SKILL.md`)**:
   - Standardized fast-forward, sync branch creation, and merge procedure.
   - Conflict resolution decision matrix for config, admin endpoints, WebUI, and wire array slots.
   - Automated PowerShell/bash regex scripts to audit Go and TypeScript files for Chinese characters (`[\p{Han}]`).
   - Pre-merge verification gates (`npm run build`, `go vet`, `go test`) and documentation rules (`sync-upstream-main.md`, `_dev.md`).
3. **Upstream Fix Skill (`.agents/skills/upstream-fix/SKILL.md`)**:
   - Defined core invariant of fork isolation: always branch fixes from `upstream-main`, never `dev`.
   - Step-by-step lifecycle: bug reproduction, minimal fix, verification, Conventional Commits, and PR submission (`gh pr create` / `git format-patch`).
   - Backport protocol: cherry-picking into `dev` with fork invariant enforcement.
4. **Documentation & Cross-References**:
   - `AGENTS.md`: Added references to all three skills in Sections 2.E and 4.
   - `docs/agents/workflows.md`: Added references to `branch-workflow`, `sync-upstream`, and `upstream-fix`.

## Result

- All three Agent Skills deployed under `.agents/skills/`.
- Complete coverage of development, sync, and bug fixing workflows.
- Repository documentation updated and aligned.
