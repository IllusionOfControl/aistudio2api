# Dev Branch Change Log

Chronological registry of commits and feature integrations merged into the `dev` branch on top of upstream base (`Mag1cFall/AIStudio2API` commit `3c8b472`).

---

## Commit Summary Table

| Date | Commit | Author | Source Branch | Document | What Was Done |
|---|---|---|---|---|---|
| **2026-10-09** | `2121ef5` | Sergey Skorokhod | `sync/upstream-main` | [`sync-upstream-main.md`](sync-upstream-main.md) | Synchronized upstream v0.2.8 (authenticated proxy & relay, remote account pairing, request usage ledger & dashboard, embeddings via Build unary calls, pure-Go WAA runtime enhancements, safety settings) |
| **2026-10-06** | `720d307` | Sergey Skorokhod | `refactor/remove-start-bat` | [`refactor-remove-start-bat.md`](refactor-remove-start-bat.md) | Removed start.bat and migrated documentation and build workflows to direct binary execution |
| **2026-10-06** | `08cfb3e` | Sergey Skorokhod | `feature/upstream-agent-skills` | [`feature-upstream-agent-skills.md`](feature-upstream-agent-skills.md) | Added agent skills suite (`branch-workflow`, `sync-upstream`, `upstream-fix`) under `.agents/skills/` and cross-referenced in AGENTS.md |
| **2026-10-06** | `38e80dd` | Sergey Skorokhod | `sync/upstream-main` | [`sync-upstream-main.md`](sync-upstream-main.md) | Synchronized upstream tool schema compatibility & validation (JSON Schema normalization, strict contracts, GET /v1/models/{model}, quota cooldown calculation, assistant prefill, search options) |
| **2026-10-03** | `9220ff6` | Sergey Skorokhod | `sync/upstream-main` | [`sync-upstream-main.md`](sync-upstream-main.md) | Synchronized upstream auth recovery and state sync (unified 401/Code 16 handling, Chrome profile fallback, generation tracking, protocol docs) |
| **2026-10-01** | `a02d32a` | Sergey Skorokhod | `sync/upstream-main` | [`sync-upstream-main.md`](sync-upstream-main.md) | Synchronized upstream v0.2.2 (Interactions API, Build native non-streaming Unary calls, thought signature normalization, admin auth) |
| **2026-09-29** | `7d3b23f` | Sergey Skorokhod | `feature/waa-auto-start` | [`feature-waa-auto-start.md`](feature-waa-auto-start.md) | Added AUTO_START (.env) and --auto-start CLI flag for WAA generation service auto-start on launch |
| **2026-09-27** | `68fd978` | Sergey Skorokhod | `docs/agents-documentation` | [`docs-agents-documentation.md`](docs-agents-documentation.md) | Extended agent documentation suite (`AGENTS.md`, `architecture.md`, `protocols.md`, `conventions.md`) for v0.2.0 features |
| **2026-09-27** | `1efd89f` | Sergey Skorokhod | `sync/upstream-main` | [`sync-upstream-main.md`](sync-upstream-main.md) | Synchronized upstream v0.2.0 (pure-Go WAA backend, dual Build channel, Omni interactions, Drive consent, Camoufox cache isolation) |
| **2026-09-26** | `0c91ef5` | Sergey Skorokhod | `docs/agents-documentation` | [`docs-agents-documentation.md`](docs-agents-documentation.md) | Added `AGENTS.md` and `docs/agents/` suite with Mermaid architecture diagrams |
| **2026-09-20** | `6e318e0` | Sergey Skorokhod | `refactor/readonly-config` | [`refactor-readonly-config.md`](refactor-readonly-config.md) | Made configuration immutable at runtime for Docker, added `HEADLESS` mode |
| **2026-09-20** | `eb81bd9` | Sergey Skorokhod | `refactor/translate-to-english` | [`refactor-translate-to-english.md`](refactor-translate-to-english.md) | Full translation of comments, logs, and error messages to English |
