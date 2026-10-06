---
branch: refactor/remove-start-bat
last_commit: 720d307
status: complete
last_activity: 06-10-2026
---

## Task

Remove `start.bat` and all its references across the repository.

## Description

Complete removal of legacy `start.bat`:
- Deleted `start.bat` script from repository root.
- Removed copying of `start.bat` into Windows release packages in `.github/workflows/build.yml`.
- Updated Web UI localization strings in `web/src/i18n.ts` (Chinese and English) to refer to generic application restart rather than executing `start.bat`.
- Rebuilt embedded frontend assets (`internal/webui/dist/`).
- Updated `README.md`, `README_ch.md`, and `docs/development.md` to reference direct binary execution (`aistudio2api.exe` / `aistudio2api`).

## Changes

- `start.bat`: Deleted.
- `.github/workflows/build.yml`: Removed `cp start.bat "$directory/"`.
- `web/src/i18n.ts`: Updated restart prompt messages.
- `internal/webui/dist/`: Rebuilt frontend static assets.
- `README.md`, `README_ch.md`, `docs/development.md`: Updated documentation and CLI instructions.
- `docs/branches/refactor-remove-start-bat.md`: Branch documentation.
- `docs/branches/_index.md`: Branch registry.

## Result

- Merged into `dev` (`720d307`).
Clean cutover with `start.bat` deleted and all callers/documentation updated to direct binary execution.
