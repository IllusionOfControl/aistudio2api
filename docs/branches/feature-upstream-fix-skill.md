---
branch: feature/upstream-fix-skill
last_commit: 3c72e71
status: ready
last_activity: 04-10-2026
---

## Task

Create an agent skill (`.agents/skills/upstream-fix/SKILL.md`) defining the procedure and guidelines for authoring, testing, and submitting bug fixes and patches intended for upstream (`Mag1cFall/AIStudio2API:main`) without leaking fork-specific refactors, plus backporting them to `dev`.

## Description

Contributors and agents working on `AIStudio2API` frequently discover bugs or edge cases in upstream protocols (e.g. MakerSuite array response anomalies, BiDi SameSite cookie deserialization, benefit tier parsing).

When creating patches intended for upstream pull requests:
1. **Fork Isolation**: The fix must be branched directly from `upstream-main` (or `upstream/main`), never from `dev`. Otherwise, hundreds of fork-specific refactors (mass English translations, Docker immutable configuration, start scripts) pollute the pull request.
2. **Minimal Blast Radius**: The diff must be surgical and strictly restricted to the bug resolution.
3. **Upstream Style Conformance**: Surrounding code style and comments in upstream files must not be gratuitously reformatted.
4. **Dual-Port / Backport**: Once verified and submitted for upstream, the fix must be cherry-picked to `dev`, with fork invariants (English comments, read-only config) enforced during the cherry-pick.

This skill equips agents with a strict workflow and checklist to execute this dual-port lifecycle cleanly.

## Changes

1. **Agent Skill (`.agents/skills/upstream-fix/SKILL.md`)**:
   - Defined core principle of fork isolation and `upstream-main` branching.
   - Authored step-by-step procedure: branch setup, failing test reproduction, surgical fix, verification, and Conventional Commit guidelines.
   - Documented PR submission and patch formatting commands (`gh pr create`, `git format-patch`).
   - Defined backport / cherry-pick procedure for integrating the verified fix into `dev`.
   - Added pre-flight readiness checklist.
2. **Documentation References**:
   - `AGENTS.md`: Added reference to `upstream-fix` skill under Git Branching Strategy.
   - `docs/agents/workflows.md`: Added reference to `upstream-fix` in Upstream Synchronization Workflow.

## Result

- Standardized Agent Skill created at `.agents/skills/upstream-fix/SKILL.md`.
- Documented upstream bug fix and backporting protocol.
