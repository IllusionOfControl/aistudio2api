---
branch: sync/upstream-main
last_commit: 38e80dd
status: complete
last_activity: 06-10-2026
---

## Task

Synchronize the `sync/upstream-main` branch with upstream commits up to `52e3128` (release v0.2.0), resolve merge conflicts while strictly preserving repository invariants (English-only code/logs, immutable read-only runtime configuration, Docker-safe behavior), and merge into `dev`.

## Description

Upstream repository `Mag1cFall/AIStudio2API` introduced major features and improvements in v0.1.7–v0.2.0:
1. **Pure-Go WAA Backend**: An in-process BotGuard VM runtime based on a tailored fork of `goja` (`internal/waa/`), running Google's official challenge interpreter, dynamic script, and snapshot proofs inside the Go process without launching or downloading Camoufox for proofs.
2. **Dual Upstream Channels (`Playground` + `Build`)**: Independent quota channels on the same account (`UPSTREAM_CHANNELS=playground,build`). Requests route across both channels with per-channel cooldowns and failover. Build channels proxy Gemini API calls via `ProxyStreamedCall` and `ProxyUnaryCall`.
3. **Omni & Interaction Stream Models**: Support for `CreateInteractionStream` wire protocol (`internal/aistudio/interaction.go`), Gemini 3.8 TTS multi-speaker modes (`VERBATIM` / `CONVERSATIONAL`), realtime translation, and realtime transcription.
4. **Camoufox HTTP Disk Cache & Profile Isolation**: Per-account disk cache directories with flock locking, stale profile auto-cleanup, and automatic Google Drive OAuth authorization during onboarding.
5. **Global Cooldown & Concurrency Refinements**: Request queuing under total candidate cooldown, exponential backoff when an account runtime is occupied by another process, and improved worker recycling.

This sync integrates all these changes, resolves 24 conflicted files, and translates all newly introduced Chinese comments, log messages, and error strings into standard concise English.

## Changes

1. **Conflict Resolution & Configuration Invariant**:
   - `internal/config/config.go`: Added `UPSTREAM_CHANNELS` and `WAA_BACKEND` alongside `HEADLESS` and `CAMOUFOX_PATH`. Discarded upstream runtime `.env` saving methods (`Save`, `atomicWrite`) to keep configuration strictly immutable at runtime.
   - `web/src/components/SettingsPanel.vue`: Preserved the read-only settings inspector; added informative displays for active `WAA Backend` (`camoufox` vs `go`) and enabled `Upstream Channels` (`playground`, `build`).
   - `internal/api/admin.go` & `internal/app/admin.go`: Preserved read-only `GET /api/config` without `PUT /api/config`.
2. **Pure-Go WAA Integration (`internal/waa/` & `internal/aistudio/runtime_go.go`)**:
   - Integrated `internal/waa/` and `internal/waa/goja/` package suite for in-process WAA challenge execution.
   - Translated all comments, error strings, and log messages across `internal/waa/` and `runtime_go.go` to English.
   - Wired `prepareWAABackend` and `newWAAWorker` in `internal/app/waa_backend.go`.
3. **Dual Upstream Channels (`internal/aistudio/channel.go` & `internal/aistudio/build.go`)**:
   - Integrated channel routing, candidate expansion, and per-channel cooldown tracking.
   - Added wire encoders and decoders for Build proxy RPCs (`ProxyStreamedCall`, `ProxyUnaryCall`).
   - Translated all comments and error strings in `channel.go` and `build.go` to English.
4. **Omni Interactions & Bidi Protocols**:
   - Integrated `CreateInteractionStream` request encoding and stream decoding in `internal/aistudio/interaction.go`.
   - Added speech metadata folding and multi-speaker mode options in `internal/aistudio/generate.go`.
   - Added realtime translation and transcription configurations in `internal/aistudio/bidi.go` and `internal/api/live.go`.
5. **Camoufox Isolation & Drive Consent**:
   - Translated cache locking and cleanup in `internal/camoufoxnative/cache.go` and `profile.go`.
   - Translated Drive authorization automation in `internal/camoufoxnative/drive.go`.
6. **Frontend & Documentation**:
   - Rebuilt frontend production bundle into `internal/webui/dist/`.
   - Updated `README.md`, `README_en.md`, `.env.example`, and `docs/development.md`.

## Result

- Clean merge into `sync/upstream-main` (`af8cc22`) and subsequent merge into `dev` (`1efd89f`).
- All Go tests pass cleanly (`go test ./...`).
- Static analysis clean (`go vet ./...` reports 0 issues).
- Frontend builds and passes all checks (`npm run typecheck`, `npm run lint`, `npm run format:check`).
- Zero Chinese characters in source code files.
- Application binary builds and runs cleanly (`go build ./cmd/aistudio2api`).

---

### Update: Upstream v0.2.2 Synchronization (2026-10-01)

**Task**: Synchronize `sync/upstream-main` with upstream commits up to `e89ba29` (release v0.2.2), resolve merge conflicts preserving read-only configuration, translate all newly introduced Chinese text to English, and merge into `dev`.

**Upstream Additions**:
1. **Interactions API**: Public `/v1/interactions` and `/v1beta/interactions` endpoints for Gemini 3.8 / Omni Interactions, structured output schema serialization, multi-turn stateless step tracking, and Gemini 3.8 TTS single/multi-speaker support.
2. **Build Native Non-Streaming Unary Calls**: `ProxyUnaryCall` support for non-streaming requests (`BUILD_NATIVE_NONSTREAM=true`) routed to Build with fallback to streaming.
3. **Thought Signature Normalization**: Normalized `thoughtSignature` attachment directly to content parts, merged adjacent thought/text fragments in Gemini output to eliminate extra newlines in SillyTavern.
4. **Admin Authentication**: Web UI login, rate limiting, SameSite session cookies, and loopback authentication separation.

**Resolution & Adaptations**:
- Preserved immutable configuration invariant (no `PUT /api/config` or runtime `.env` mutations).
- SettingsPanel retains read-only status displays for Admin Auth and Build Native Nonstream.
- Translated 24 files with 72 Chinese comment/log lines into concise technical English.
- Adapted `internal/app/admin_config_test.go` to test read-only config loading and restart flag detection.
- Merged into `dev` (`a02d32a`). All tests (`go test ./...`) and lint checks pass cleanly.

---

### Update: Upstream Auth Invalidation Recovery & Account State Sync (2026-10-03)

**Task**: Synchronize `sync/upstream-main` with upstream commit `7c9da1d` ("fix: 修复登录失效恢复与账户状态同步"), resolve merge conflicts preserving English-only documentation and code invariants, and merge into `dev`.

**Upstream Additions**:
1. **Unified Authentication Recovery**: Unified login redirects, expired signature cookies, HTTP 401, and Live protocol Code 16 into a single recovery pipeline covering worker pre-warming, on-demand startup, standard RPCs, protected RPCs, and Live connections.
2. **Account Egress Renewal & Dynamic Chrome Matching**: Prioritized renewing cookies on the same egress; added fallback to match the original account email and Gaia ID in current Chrome profiles if saved OAuth credentials are rejected.
3. **Generation Tracking & Concurrent Commit**: Concurrently invalidating requests reuse a single commit result; login credential replacement advances auth generations so stale responses do not overwrite newer states.
4. **Account State Synchronization**: Failed recovery marks accounts as `auth_required`, broadcasting status to administrative events and evicting them from subsequent scheduling until restored.
5. **Protocol Documentation**: Updated protocol specifications for auth recovery, concurrency, and state write-back in `docs/protocol.md`.

**Resolution & Adaptations**:
- Translated updated protocol documentation in `docs/protocol.md` to English, resolving merge conflicts.
- Verified all Go code comments, logs, and error strings remain strictly in English.
- Validated all Go tests (`go test ./...`), vet (`go vet ./...`), and builds (`go build ./...`) pass cleanly.
- Merged into `dev` (`9220ff6`).

---

### Update: Upstream Tool Schema Compatibility & Validation (2026-10-06)

**Task**: Synchronize `sync/upstream-main` with upstream commits up to `51883d8` ("fix: 完善工具 Schema 与四协议调用兼容"), resolve merge conflicts preserving English-only code, documentation, and read-only configuration invariants, and merge into `dev`.

**Upstream Additions**:
1. **JSON Schema Normalization & Complex Tool Compatibility**: Recursive `null` cleanup in optional schema fields with data `null` value retention (`example: null`, constants, property names). Support for boolean schemas (`true`/`false`), `const` validation, `items: false` (empty array `maxItems=0`), and string `not` constraints. Removed forced Build diversion for standard tools; complex tools with union branches (`anyOf`/`oneOf`) and array definitions now execute directly on Playground channels.
2. **Strict Tool Contracts & Validation (`internal/aistudio/tool_contract.go`)**: Strict argument schema validation (`jsonschema/v6` with ECMA regex engine via `regexp2`). Support for `tool_choice` modes (`required`, `auto`, `none`, `validated`), required tool enforcement before turn finish, and `parallel_tool_calls: false` single-call constraint.
3. **Public API Enhancements (`internal/api/`)**:
   - Implemented `GET /v1/models/{model}` and `GET /v1beta/models/{model}` endpoints for OpenAI and Anthropic formats with canonical ID and alias resolution.
   - Added `web_search_options` support (`search_context_size`, `user_location`, `allowed_domains` with `site:` filters) across generation pipelines.
   - Anthropic assistant prefill support (prefix passed to continuation instructions, response returning post-prefix text).
   - Anthropic thinking `disabled` support (hides thought body while maintaining thought signatures).
   - Automatic conversation turn truncation (`Truncate`) according to authoritative token count while preserving tool call/result pairs.
4. **Quota Cooldown Refinement (`internal/aistudio/quota.go`)**: Cooldown cycle evaluation using `quota_unit`, `quota_limit`, `quota_metric`, and error messages. Precise retry delay parsing via `RetryInfo` and `Retry-After`. Scoped vs global cooldown differentiation.

**Resolution & Adaptations**:
- Resolved merge conflicts in 7 Go files (`internal/aistudio/build.go`, `generate.go`, `quota.go`, `schema.go`, `tools.go`, `internal/api/openai.go`, `responses.go`) and 3 documentation files (`README.md`, `docs/development.md`, `docs/protocol.md`).
- Translated all newly introduced comments, log messages, and error strings across all packages to standard technical English (zero Chinese characters in Go codebase).
- Updated English documentation (`README.md`, `docs/development.md`, `docs/protocol.md`) and synchronized Chinese documentation (`README_ch.md`).
- Validated with `go test ./...` (all passing), `go vet ./...` (0 issues), `npm --prefix web run typecheck` (0 errors), and `go build ./cmd/aistudio2api`.
- Merged into `dev` (`38e80dd`).

---

### Update: Upstream Full Protocol Compatibility & v0.2.8 Synchronization (2026-10-09)

**Task**: Synchronize `sync/upstream-main` with upstream commits up to `5ec39b8` (release v0.2.8), resolve all merge conflicts preserving read-only configuration and English-only code invariants, translate all newly introduced comments and error messages to English, and merge into `dev`.

**Upstream Additions**:
1. **Authenticated Proxy & Relay (`internal/proxydial`)**:
   - Support for HTTP, HTTPS, and SOCKS5 upstream proxies with username/password credentials.
   - Local loopback SOCKS5 relay service for Camoufox to prevent credential leakage into browser profiles.
2. **Remote Account Pairing (`internal/setup`, `internal/api/pairing.go`)**:
   - Remote pairing tokens with 10-minute expiration (`POST /api/pairing`, `POST /api/pairing/accounts`).
   - Remote account onboarding via `aistudio2api setup --remote <origin> [--token <TOKEN>]`.
3. **Request Usage Ledger & Analytics Dashboard (`internal/requestdb`)**:
   - High-performance local SQLite request ledger in `runtime/requests.db`.
   - Hourly and daily rollups, latency percentiles, dimensional aggregation (model, account, channel, protocol, state), CSV export, and optional request/response body capture (`REQUEST_BODY_LOG=true`).
   - Embedded Vue 3 Usage analytics dashboard tab with interactive time range picker and charts.
4. **Full Protocol Compatibility & Embeddings**:
   - Embeddings API (`/v1/embeddings`, `:embedContent`, `:batchEmbedContents`) via Build `ProxyUnaryCall` with batch chunking.
   - Realtime Live & Music over Build WebChannel (`internal/aistudio/build_bidi.go`).
   - Assistant turn tail continuation (`internal/aistudio/turns.go`) and system-instruction-only generation support.
   - Upstream streaming errors before first event return immediate HTTP 4xx/5xx status before SSE headers are committed.
5. **Pure-Go WAA Runtime Enhancements (`internal/waa`)**:
   - Precompiled prompt program and gzipped Firefox 152 shape table (`firefox152.json.gz`).
   - Full `Intl` constructor suite (`intl.js`) with localized date formatting and CLDR collation.
6. **Safety Settings & Stop Sequence Parity**:
   - Unified `safetySettings` wire encoder (`internal/aistudio/safety.go`) supporting standard harm categories and threshold enums.
   - Parallel `CountTokens` execution for accurate token usage reporting on stop sequence termination.

**Resolution & Adaptations**:
1. **Immutable Configuration Preserved**: Dropped upstream `.env` mutation logic (`Config.Save()`), dynamic update handler (`UpdateRuntimeConfig`), and form inputs with save buttons. `SettingsPanel.vue` continues to serve strictly as a read-only configuration inspector, updated to display `REQUEST_BODY_LOG` and `AUTO_START`.
2. **English-Only Code & Logs**: Identified and translated 668 lines of Chinese comments, log messages, CLI output, and error strings across all Go and TypeScript files into idiomatic English. Zero Chinese characters remain in source files outside of i18n dictionaries.
3. **Frontend Build & Validation**: Validated Vue 3 codebase with `npm run typecheck` (0 errors), `npm run lint` (0 errors), `npm run format:check`, and rebuilt production bundle via `npm run build` embedded into binary.
4. **Backend Verification**: Ran static analysis (`go vet ./...`, 0 issues) and full test suite (`go test ./...`, all packages passing). Verified binary compilation via `go build ./cmd/aistudio2api`.