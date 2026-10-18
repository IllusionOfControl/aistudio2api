# Development & Contributing

AIStudio2API uses Go to directly invoke Google AI Studio's internal MakerSuite private protocol, presenting accounts, models, cooldowns, and request statuses via an embedded Vue 3 management dashboard. Request processing, streaming decoding, account scheduling, and public protocol adaptation are all performed in-process within Go; Camoufox is retained solely for official WAA initialization and fresh proof generation workflows (when `WAA_BACKEND=go`, this is also handled in-process in Go; see [WAA Implementation](waa.md)).

The management listener, public APIs, account pool, protocol runtime, and embedded Vue management UI reside within the same process. The generation service is a stoppable, recreatable public API service instance; for raw JSON+protobuf, WebChannel, and WAA formats, see [protocol.md](protocol.md).

## 1. Environment, Initial Configuration, and Startup

| Scenario | Required Components | Description |
| --- | --- | --- |
| Release execution | `aistudio2api` | Automatically prepares Camoufox on first run; does not require Python, Node.js, or Playwright |
| Source execution | Go 1.26.0+, Node.js 22.13+ or 24+, accompanying npm | Node.js is only used to build the Vue management interface |

Windows users can directly run `aistudio2api.exe` from a release package or build from source using Go and Node.js. Once started, the program automatically opens the management dashboard while the generation service remains stopped initially; account login, log inspection, and starting/stopping the generation service are all performed in this interface.

When frontend or Go source code changes require a rebuild, rebuild the frontend via `npm run build` in `web/` and recompile the binary via `go build -o aistudio2api.exe ./cmd/aistudio2api`.

Initial startup from source:

```powershell
cd web
npm ci
npm run build
cd ..
go run ./cmd/aistudio2api
```

The "Accounts" page in the management dashboard provides bulk Chrome import, browser login, and remote pairing, as well as re-login, immediate refresh, editing, enabling/disabling, and deletion. Browser login is completed in isolated Camoufox and automatically detects the email. `setup` provides the following command-line import entries:

| Entry Point | Command | Use Case |
| --- | --- | --- |
| Scan local Chrome | `aistudio2api setup` | Interactively select importable profiles |
| Specific Chrome account | `aistudio2api setup --profile <PROFILE>` or repeated `--email` | Deterministic batch imports |
| Isolated login | `aistudio2api setup --login` | Manually complete Google login in a visible Camoufox window |
| File import | `aistudio2api setup --storage-state <file>` | Import Playwright storage state structure |

`setup` also accepts the following flags:

| Flag | Purpose |
| --- | --- |
| `--chrome-root <DIR>` | Specify Chrome User Data root directory |
| `--proxy <URL>` | Pin to account initialization, WAA, and request traffic |
| `--locale <LOCALE>` | Set account locale, defaults to current machine |
| `--timezone <TIMEZONE>` | Set account timezone, defaults to current machine |
| `--remote <ORIGIN>` | Upload accounts to remote instance (HTTPS required for non-loopback) |
| `--token <TOKEN>` | Pairing token for remote instance; read from stdin if omitted |

`--storage-state`, `--login`, and Chrome import parameters define file import, isolated login, and browser import modes respectively. Isolated login uses `--login`. `setup` requires `AISTUDIO_AUTH_STATES` to point to an account directory when saving locally.

When `--remote` is specified, Chrome import, isolated login, and file import are performed locally as usual, and results are uploaded one by one using the pairing token generated from "Add remotely" on the remote instance Accounts page; isolated login also uploads the Camoufox fingerprint. Remotely added accounts use the remote instance default proxy; `--proxy` is only used for local import and login.

`--proxy` fixes the proxy for the new account's initialization, WAA, and generation requests, accepting HTTP, HTTPS, or SOCKS5 URLs with authentication in `user:password@`; when omitted, import and login use the first proxy in `PROXY`, and no dedicated proxy is assigned to the account. `--locale` and `--timezone` configure the account environment; Chrome import reads the profile's preferred language when unspecified. Camoufox is located in order: environment variable `CAMOUFOX_PATH`, `runtime/camoufox/`, adjacent directory named `camoufox/`, and Windows native Camoufox cache. If none exist, the pinned version for the current platform is downloaded automatically.

Routine startup only requires running the binary or Go entry point, then starting the generation service from the management dashboard:

```powershell
./aistudio2api.exe
go run ./cmd/aistudio2api --listen 127.0.0.1:2048 --open-ui
```

Routine service execution accepts the following flags:

| Flag | Purpose | Default Source |
| --- | --- | --- |
| `--auth <PATHS>` | Override account files, directories, or comma-separated paths for this process | `AISTUDIO_AUTH_STATES` |
| `--listen <HOST:PORT>` | Override listen address for management dashboard and API | `LISTEN_ADDR` |
| `--proxy <URL>` | Override global proxy used when creating generation service instances | `PROXY` |
| `--open-ui` | Open management dashboard in browser upon launch | `true` when launched without arguments |

The management dashboard and `/api` control plane continue running even when the generation service is stopped. Stopping the generation service cancels active generation requests and closes WAA workers; restarting it re-reads account model catalogs. Closing the console window or pressing `Ctrl+C` exits the entire management process.

## 2. Directories, Components, and Runtime Dependencies

```text
cmd/aistudio2api/        Thin entry point, calls app.Run in internal/app
internal/app/            Configuration, account assembly, auth refresh, signals, generation service instances, scheduling, lifecycle
internal/setup/          CLI setup execution for Chrome, file, and browser login
internal/aistudio/       Accounts, MakerSuite, WAA, models, tools, uploads, RPC client, protocol encoding
internal/api/            Protocol routing, request decoding, SSE conversion, admin endpoints
internal/camoufoxnative/ Camoufox WebDriver BiDi, process lifecycle, profile isolation, and login
internal/waa/            Pure-Go BotGuard VM runtime, Firefox DOM simulation, Goja JavaScript VM
internal/proxydial/      Upstream proxy dialing and local SOCKS5 relay for authenticated proxies
internal/requestdb/      Request usage ledger and optional body SQLite storage
internal/pricing/        Gemini API pricing tables used for usage cost estimation
internal/chromeauth/     Chrome profile discovery, DBSC renewal material, DPAPI and app-bound decryption
internal/config/         Configuration loading, validation, and immutable settings
internal/webui/          Static frontend embedding and SPA fallback routing
web/                     Vue 3, TypeScript, Vite, and Tailwind CSS source code
docs/                    Development runbooks and private protocol specifications
auth/                    Per-account configuration, credentials, and recoverable runtime states
runtime/camoufox/        Camoufox runtime used in releases
runtime/requests.db      Request usage ledger and optional captured bodies
```

Primary dependency direction:

```text
cmd/aistudio2api
  -> internal/app
       -> internal/api
       -> internal/aistudio
       -> internal/setup
       -> internal/camoufoxnative
       -> internal/config
       -> internal/webui

internal/setup
  -> internal/aistudio
  -> internal/camoufoxnative
  -> internal/chromeauth
  -> internal/config
```

The request processing pipeline maintains a strict unidirectional flow:

```text
HTTP route
  -> client protocol decoder
  -> canonical request
  -> capability-aware account lease
  -> AI Studio array encoder
  -> per-account WAA proof
  -> fingerprinted Camoufox GenerateContent transport
  -> incremental response decoder
  -> canonical events
  -> client protocol response
```

WebSocket endpoints follow the same layering: `internal/api` decodes the public protocol, `internal/app` binds accounts and runtime states, and `internal/aistudio` performs WebChannel and canonical event conversions. Public adapters consume only canonical requests and events; account files, WAA objects, raw arrays, and resource stickiness are managed by `internal/aistudio` and `internal/app`.

Camoufox is managed directly by Go via WebDriver BiDi. When starting the data plane, the service prepares isolated, headless, resident account runtimes according to `WARM_WORKER_LIMIT` and `WARM_STARTUP_CONCURRENCY`, replacing the least recently used idle runtime when other account capabilities are needed. Headless runtimes throttle page rendering to 1 frame per second. WebGL is forcibly enabled across all hosts, sampling a Windows graphics card configuration from the account fingerprint. On Linux, Camoufox loads only the Windows fonts bundled in the release package (hinting set to `hintslight`), resolves font names per Windows Firefox font preferences and Windows font substitution tables, supplements the font whitelist with font families sharing the same font files, and enables WebGPU, Windows 10 scrollbar styles, and 2-channel AudioContext; `window.innerHeight` is computed as `window.outerHeight` minus the Windows browser UI height of 57, and global site storage limit is fixed at 50 GiB. On headless Linux hosts without `DISPLAY`, each Camoufox instance runs on a dedicated Xvfb virtual display started by the service with software Mesa GLX rendering, and the Xvfb instance terminates when Camoufox exits. Each runtime uses a temporary profile that is deleted upon shutdown; the HTTP disk cache is written to `camoufox-cache/` in the account directory, allowing the same account to reuse official static assets across restarts. A second concurrent runtime for the same account uses the cache inside its temporary profile. The process creating the temporary profile holds a lock file inside the profile, and runtime assembly deletes orphaned profiles whose locks have been released. On Windows, each Camoufox process tree is assigned to a Job Object held by the service process, ensuring all child processes terminate when the parent exits. Each runtime triggers GenerateContent on the official page and intercepts the request before network transmission to acquire the official WAA service and dynamic headers; subsequent operational payloads are encoded by Go, and after synchronizing prompt state and generating a fresh proof, sent via native `fetch` within the same fingerprinted page. The response stream is returned in chunks to Go over WebDriver BiDi. WebDriver BiDi commands execute serially on the same connection with a maximum timeout of 150 seconds per command; when a request is canceled or times out, the call returns immediately, and the background routine finishes reading the command's response before sending subsequent commands, allowing the runtime to continue serving future requests. Other MakerSuite, Drive, and media control plane requests use the Go HTTP transport pinned to the account's egress proxy.

When `WAA_BACKEND=go`, Camoufox is not located, downloaded, or launched. Each account runtime requests the official web page and `GetLoggingContext` within the service process to obtain public request headers, calls `Waa/Create` to obtain a challenge, downloads and caches the interpreter to `auth/.waa-interpreters/` by hash, and executes the program within the `goja` fork and Firefox-shaped host in `internal/waa`. Protected requests carry Firefox request headers and account cookies, sent by Go HTTP transport via the account's pinned egress, and response cookies are written back to account state. Browser login on the Accounts page prepares Camoufox on demand upon first use. For complete details on the architecture, host environment, lifecycle, data files, and upstream tracking methods, see [WAA Implementation](waa.md).

## 3. Configuration, Accounts, and Persistent State

The application reads optional `.env` configuration from the current working directory; process environment variables override corresponding settings:

| Variable | Description | Default |
| --- | --- | --- |
| `AISTUDIO_AUTH_STATES` | Account file, directory, or comma-separated multiple paths | `auth` |
| `LISTEN_ADDR` | HTTP service listen address | `127.0.0.1:2048` |
| `PROXY_API_KEY` | Public API key, multiple keys separated by commas | empty |
| `AUTO_START` | Automatically start generation service once manager process is ready; logs ERROR on failure, manager keeps running | `false` |
| `REQUEST_BODY_LOG` | Save public API POST request and response bodies, truncated to 64 KiB each, keeping latest 1000 | `false` |
| `LOG_FILE` | Simultaneously write service logs to `runtime/logs/aistudio2api.log`, rotated at 10 MB per file | `true` |
| `LOG_MAX_TOTAL_MB` | Total size limit for log directory in MB, deletes oldest files when exceeded (minimum 20) | `500` |
| `ADMIN_AUTH_ENABLED` | Enable admin console username/password authentication | `false` |
| `ADMIN_USERNAME` | Admin console username | `admin` |
| `ADMIN_PASSWORD` | Admin console password; required when authentication is enabled | empty |
| `PROXY` | HTTP, HTTPS, or SOCKS5 proxy shared across setup and accounts without a dedicated proxy; multiple proxies form a proxy pool | empty |
| `INIT_TIMEOUT` | Per-account initialization timeout | `2m` |
| `REQUEST_TIMEOUT` | Request execution timeout, multiplied by continuation rounds for auto-continuation models | `30m` |
| `WARM_WORKER_LIMIT` | Resident prewarmed accounts | `5` |
| `MAX_ACTIVE_WORKERS` | Maximum concurrent workers during peak load | `10` |
| `WARM_STARTUP_CONCURRENCY` | Concurrently cold-started Camoufox workers, shared by prewarming and scaling; unlimited when `WAA_BACKEND=go` | `5` |
| `PER_ACCOUNT_CONCURRENCY` | Concurrent request slots per account | `2` |
| `ROUTING_STRATEGY` | `round-robin` or `fill-first` | `round-robin` |
| `UPSTREAM_CHANNELS` | `playground,build` comma-separated | `playground,build` |
| `MODEL_MAPPING` | Comma-separated alias:model_id mappings | empty |
| `BUILD_NATIVE_NONSTREAM` | Prioritize native Build unary calls for non-streaming requests | `true` |
| `WAA_BACKEND` | `camoufox` or `go` | `camoufox` |
| `TEMPORARY_CHAT` | Use temporary chat for WAA prewarm page | `false` |
| `HEADLESS` | Whether Camoufox browser runs in headless mode | `true` |
| `CAMOUFOX_PATH` | Explicit Camoufox executable path | empty |

`GET /api/config` exposes the active runtime configuration (read-only). Configuration is loaded once at startup from environment variables or `.env` and does not support runtime mutation via the API:

| Field | Meaning |
| --- | --- |
| `auth_states`, `proxy`, `init_timeout`, `request_timeout` | Values used upon generation service startup |
| `warm_worker_limit`, `max_active_workers`, `warm_startup_concurrency`, `per_account_concurrency` | Capacity parameters applied immediately |
| `temporary_chat`, `waa_backend`, `upstream_channels`, `build_native_nonstream` | WAA and upstream channel configurations |
| `model_mapping` | Model alias mappings |
| `auto_start` | Whether generation service auto-starts when manager starts |
| `request_body_log` | Whether request and response bodies are captured |
| `log_file`, `log_max_total_mb` | File logging configuration and rotation cap |
| `admin_auth_enabled`, `admin_username` | Admin authentication configuration |
| `admin_password_set` | Whether an admin password is configured |
| `listen_addr`, `proxy_api_key` | Management listener configuration |
| `active_listen_addr`, `active_proxy_api_key` | Values actively held by the management process |
| `active_proxy` | Active egress proxy used by the service |
| `proxy_pool` | Proxy pool entries: address, availability, bound accounts, last check time, and error |
| `management_restart_required` | Saved listener address, API key, or admin login configuration differs from active process |
| `service_restart_required` | Saved generation service configuration differs from active instance |

When `PROXY` contains multiple proxies separated by commas, they form a proxy pool. When starting the generation service, each proxy is tested against `https://aistudio.google.com/`, and failing proxies are excluded from assignment. Accounts without dedicated proxies retain healthy bindings from runtime state, while remaining accounts are bound to available proxies with the fewest accounts. When adding or importing accounts, login uses the proxy assigned to that account. Network errors during generation or worker startup (Go transport errors, page `NetworkError`, or connection failures during AI Studio load) count against the proxy; a single success resets the counter. After 3 consecutive failures, if other available proxies exist, the proxy is marked unavailable and its accounts are rebound to available proxies, rebuilding header clients and workers under an exclusive lease. The pool rechecks all proxies every 5 minutes, marking failed proxies unavailable, rebinding accounts from unavailable proxies to healthy ones, and returning recovered proxies to rotation. Egress priority is: account proxy, proxy pool binding, system proxy, direct connection.

When admin authentication is enabled, `/api` uses an independent `HttpOnly`, `SameSite=Strict` session cookie valid for 12 hours. Logging out revokes the session and terminates its management SSE subscription. When admin authentication is disabled, the management API enforces loopback origin and host validation. Remote management should be proxied via HTTPS reverse proxy, preserving `Host` and setting `X-Forwarded-Proto: https`. Access keys for the generation API are configured separately.

Configuration is loaded once from environment variables and `.env` upon startup and remains strictly read-only during execution. The Web UI Settings panel functions as a read-only configuration inspector.
Generation service startup follows this sequence (in code, `generation` denotes a single generation service instance created by a Stop/Start cycle):

```text
POST /api/control/stop
  -> Cancel LAUNCHING, active requests, and background scaling worker starts
  -> Wait for model catalog refresh to exit and shut down active workers
  -> Management listener continues serving /api and Web UI

POST /api/control/start
  -> Complete cleanup of previously stopped instance
  -> Re-read .env and apply command-line --auth/--proxy overrides
  -> Create and activate new generation service instance
  -> Initialize in-memory catalog from current generation's CachedModels
  -> Concurrently refresh all enabled ready/busy accounts
  -> Cold generation waits for first non-empty verified catalog
  -> Start first WAA worker and transition to RUNNING; if no warmable account is ready before first sync completes, re-warm after sync completes
  -> Remaining catalog sync and warm pool pre-warming continue in the background
```

When configuration reading, validation, instance creation fails, or cancellation occurs before activation, the stopped instance remains unchanged. If startup fails after switching to a new instance, the instance enters `STOPPED` and the management UI returns a structured error. The model catalog is kept in the memory of the current generation instance; new instances created by standard Stop/Start perform a cold start from empty `CachedModels`. When the current instance already has a verified cache, `trackedService.Start` directly uses that cache to warm workers while continuing to refresh all accounts in the background.

Catalog synchronization shares a context (Go cancellation signal) with the active generation service. Startup failures, cancellations, or Stop operations wait up to 2 seconds for sync goroutines to exit; incomplete lifecycle transitions (start or stop operations) wait up to 12 seconds. Timeouts and worker cleanup errors are retained in the error chain via `errors.Join`.

Each account directory contains:

| File | Content | Lifecycle |
| --- | --- | --- |
| `account.json` | label, enabled, proxy, locale, timezone | Written when creating or editing an account |
| `storage-state.json` | Cookies, localStorage, and optional Chrome OAuth/DBSC refresh materials | Atomically written back after merging `Set-Cookie` or auth refresh |
| `camoufox-fingerprint.json` | Account's fixed browser fingerprint, language, and timezone | Generated on first run; empty values, window geometry, fonts, voices, WebGL, and media devices normalized per Camoufox launcher rules; reused across re-logins and WAA runtimes |
| `runtime-state.json` | Benefit tier, model access status, cooldowns, and resource-account bindings | Atomically written back after tier sync, first model result, or resource changes |
| `camoufox-cache/` | Camoufox HTTP disk cache for this account, capped at 256 MB | Exclusively written while WAA runtime is active; deleted along with account directory |

In `runtime-state.json`, `model_access` values are `{state, checked_at, reason?}` with `verified` representing success; `cooldowns` values are `{until, reason?}`; `resources` values store kind, name, mime, size, purpose, created_at, and optional video metadata. Drive files, Veo operations, video artifacts, and Bidi resumption tokens maintain sticky affinity to their creating account. Veo operations additionally persist public video object metadata (model, duration in seconds, file size, UTC creation timestamp), allowing subsequent polling after service or process restarts to project identical fields.

Active request locks protect cross-process account leases, while short transaction locks guard `runtime-state.json` merge writes. Short transaction locks reside at `auth/.leases/<account>.runtime.lock`, retrying at 25ms intervals up to 2 seconds; once acquired, disk state is re-read, target fields modified, files atomically replaced, and in-memory caches and resource indices synchronized. Resource transactions taking a context return early if cancelled or upon deadline expiration.

Account updates treat the atomic write of `account.json` as the persistent commit point. `internal/app` first prepares a `pending` (uncommitted) fixed egress, shuts down the old worker, locks the account's worker configuration, and calls `AccountLease.SaveConfig`; once saved, worker configuration and fixed egress are committed in sequence. If preparation, shutdown, or saving fails, pending updates are discarded; lease release errors after a successful save are returned as-is, and published configurations remain active.

Account scheduling filters candidates by models and methods returned from real-time per-account `ListModels`, then selects workers that are ready with available concurrency slots. `ROUTING_STRATEGY=round-robin` alternates across candidate accounts for each model, resuming from the last selected account ID; `fill-first` persistently uses the first available account ordered by ID, switching only when concurrency slots are full, in cooldown, or unavailable. Generation request candidates are combinations of accounts and enabled `UPSTREAM_CHANNELS`: Playground evaluates candidates via `ListModels` and benefit tiers, while Build evaluates via the Gemini API model catalog returned by the Build proxy and the same benefit tiers; round-robin and sticky selection progress across combinations ordered by account ID and channel sequence, with cooldowns tracked per channel (`build:<model>` for Build). When one channel enters cooldown, the same account can continue on another channel; when all enabled channels enter cooldown, requests queue or return 429 according to cooldown rules; `-build` models use the Build coding assistant channel `assistant` only, with cooldown scope set to the `-build` model ID (see [Build Channel](build.md)). Concurrency slots, workers, and WAA are shared per account; token counting, Live, Veo, transcription, and Drive file references use Playground RPCs. Each account leases at most `PER_ACCOUNT_CONCURRENCY` request slots concurrently; the first request acquires the cross-process file lock, and the last request releases it. WAA proofs are generated serially by account workers, while `GenerateContent` is transmitted concurrently and read as a stream over the same Camoufox page (or via account runtime Go HTTP when `WAA_BACKEND=go`); requests generate Authorization using the worker's current cookies before dispatch, and once response headers arrive, worker cookies are atomically synchronized to persistent account storage. Cookies from other MakerSuite HTTP responses are merged with latest account state upon header arrival. Unpinned requests encountering retriable 401, 403, 404, 429, 5xx, connection interruptions before receiving response, or single-account initialization timeouts can switch to untried accounts with identical capabilities before the first upstream semantic event is emitted; Drive references are temporarily copied to the executing account on demand, while explicit accounts and Veo operations maintain sticky bindings. Chrome-imported states retain refresh materials: on HTTP `401`, a single refresh is attempted via the same fixed egress, the account's WAA runtime is rebuilt, and the request is replayed.

Worker capacity is constrained by warm pool target, active worker ceiling, and per-account concurrency. When active worker count is below `MAX_ACTIVE_WORKERS`, new workers are launched and published directly. When capacity is full and idle legacy instances exist, a pending replacement worker is started before shutting down the old instance and publishing the replacement; idle instances whose requested model is in cooldown (global or model quota limit) are prioritized, with the least recently used among them selected first, followed by the oldest idle instances overall. Multiple cooling instances can be replaced in parallel, each reserving an independent old instance, resulting in a brief coexistence of old and new processes during startup. Existing workers continue serving if replacement startup fails or is cancelled. Requests only claim cold accounts when capacity slots are available; when no slot is immediately available, the account is released and reclassified to be serviced by an idle warm worker or a newly vacated slot. Requests with schedulable accounts but no free slots queue in first-come, first-served order under matching selection criteria, waking on lease releases or worker state changes until request timeout. When all candidate accounts are in cooldown, requests whose earliest recovery time is within 1 minute queue waiting for recovery, while requests with later recovery times immediately return 429 with the earliest recovery time in the error payload. Workers exceeding `WARM_WORKER_LIMIT` that remain idle for 5 minutes are closed, maintaining the warm pool target. When concurrent recycling of an old worker and pending worker fails, both process and lease are retained as cleanup pending and consume a capacity slot; subsequent Stop operations will retry cleanup. When an account's WAA runtime lease is held by another process, it is excluded from warming and scheduling candidates, re-probing after 5 seconds initially with doubled backoff up to 1 minute, logging once per contention period; requests requesting that account specifically or left with only contended accounts return an account-in-use error.

Failure recovery waits for active requests on the same account to release their leases; during this waiting period, new requests are suspended for that account. Client cancellations only terminate their own request. Warm-up runs are submitted once the official Run button is activated, and requests re-check account cooldown status immediately before transmission.

Quota cooldowns are evaluated by `quota_unit`, `quota_limit`, `quota_metric`, and error messages in order. Explicit daily limits reset at midnight US Pacific Time the next day; minute limits reset according to valid `window_start_time`; generic 429 errors cool down for one minute. Tier limits without a specified retry delay (`You've reached your usage limit`) escalate cooldowns through 1, 5, 15, 30, and 60 minutes based on consecutive occurrences for the same account and scope; concurrent failures before expiration retain the current deadline, resetting to 1 minute after a successful request on that scope. When a subscription model hits quota limits, the background routine immediately refreshes that account's tier and catalog (at most once per minute per account). Retry delays provided by `RetryInfo` and `Retry-After` take precedence. When all quota items in the error report `limit: 0`, the cooldown type is categorized as no quota. Global scope is confirmed by quota metadata, while other errors only cool down the failed model or capability scope; post-body quota errors and Live session errors follow the same rules.

Account states:

| State | Meaning |
| --- | --- |
| `ready` | Authentication valid and schedulable capacity available |
| `busy` | Account undergoing exclusive operation, auth refresh, or active request; scheduling evaluates remaining slots via `PER_ACCOUNT_CONCURRENCY` |
| `cooldown` | Account's global `*` cooldown is active; model `scope` cooldowns only affect candidate classification for matching requests |
| `auth_required` | Account-level authentication failure; re-login or credential renewal required |
| `unavailable` | Account cannot be used by current runtime |
| `disabled` | Account configuration disabled |

Authentication results carry `authGeneration` and `checkedAt`, applied only when account object, authGeneration, and chronological sequence match; successful results at the same timestamp take precedence over failures. Model success and cooldown persistence use independent `modelAccessGeneration` and `checked_at`; modelAccessGeneration increments on catalog changes, retaining verified state when success already exists at the same timestamp.

Model access scopes:

| Operation | Scope | Success and Failure Semantics |
| --- | --- | --- |
| Standard GenerateContent | `<modelID>` | Writes `verified` upon arrival of canonical `EventFinish`; Code 7 retains existing record |
| CountTokens | `count-tokens:<modelID>` | Success clears cooldown for this scope; model `verified` remains unchanged |
| Transcribe | `<modelID>` | Writes `verified` on non-empty text or segments; Code 7 retains existing record |
| Live text-only | `<modelID>` | Writes `verified` on setup success; each `SendText` initiates a qualification check, updated upon `turn_complete` |
| Live audio or image | `bidi-media:<modelID>` | `SendMedia` initiates a qualification check and updates media scope |
| Robotics | `bidi-media:<modelID>` | `SendText` initiates a qualification check, updated upon `turn_complete` |

`ModelAccessKey(scope, model)` removes the `models/` prefix; empty scope returns the canonical model ID, and non-empty scope returns `<scope>:<canonicalModelID>`. Bidi setup uses the account lease timestamp, while each qualifying turn within a session is assigned a strictly increasing attempt timestamp consumed by `turn_complete`. Standard streaming generation writes `verified` upon arrival of canonical `EventFinish`; prior text, reasoning, tool, usage, and initial events serve output and latency metrics, while mid-stream disconnections, cancellations, or errors preserve existing verification states.

Model catalog refresh runs concurrently across all enabled ready/busy accounts. Accounts returning errors or empty catalogs enter a pending ID set within the generation instance; each non-empty result immediately updates the shared catalog, account state, and warms additional workers during `RUNNING`. After the initial fan-out completes, a single 30-second ticker concurrently re-refreshes eligible pending accounts; a 10-minute ticker in the same loop concurrently reads `GetAiStudioBenefitTier` for all enabled ready/busy accounts, each occupying a normal concurrency slot (accounts with full slots skip the round; when tiers change, the existing catalog is written back with the new tier and account/model snapshots are published). Tasks in cooldown are retained until expiration; accounts requiring login, disabled, unavailable, or deleted exit the retry loop, re-syncing via account updates once re-authenticated or re-enabled. Failure logs record account and reason; successful recovery logs model count. `modelRevision` tracks account and configuration changes, ensuring the generation service validates application of the current revision before accepting traffic.

Authentication state contains persistent credentials and device-bound materials stored in a secure local directory. Commits, issues, CI logs, and standard logs use sanitized payloads, preserving field structures while replacing cookies, tokens, proofs, emails, account IDs, prompts, response bodies, and raw wire frames.

## 4. Go Protocol Layer, Public Endpoints, and Vue Management

Public endpoints uniformly consume the same real-time model catalog and canonical events:

| Protocol | Endpoints |
| --- | --- |
| OpenAI Chat | `GET /v1/models`, `GET /v1/models/{model}`, `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses`, `POST /v1/responses/input_tokens`, `POST /v1/responses/compact`, `GET`/`DELETE /v1/responses/{id}`, `GET /v1/responses/{id}/input_items`, `POST /v1/responses/{id}/cancel` |
| Gemini Interactions | `POST /v1beta/interactions`, `GET`/`DELETE /v1beta/interactions/{id}`, `POST /v1beta/interactions/{id}/cancel` (same paths under `/v1/interactions`) |
| OpenAI & Anthropic Files | `POST`/`GET /v1/files`, `GET`/`DELETE /v1/files/{file}`, `GET /v1/files/{file}/content` |
| OpenAI Media | `POST /v1/images/generations`, `POST /v1/images/edits`, `POST /v1/audio/speech`, `POST`/`GET /v1/videos`, `GET`/`DELETE /v1/videos/{id}`, `GET /v1/videos/{id}/content` |
| OpenAI Transcribe | `POST /v1/audio/transcriptions`, `POST /v1/audio/translations` |
| OpenAI Embeddings | `POST /v1/embeddings` |
| Anthropic | `POST /v1/messages`, `POST /v1/messages/count_tokens` |
| Gemini | `GET /v1beta/models`, `GET /v1beta/models/{model}`, `POST /v1beta/models/{model}:generateContent`, `:streamGenerateContent`, `:countTokens`, `:embedContent`, `:batchEmbedContents`, `:predictLongRunning`, `GET /v1beta/operations/{id}` |
| Gemini Files | `POST /upload/v1beta/files`, `GET /v1beta/files`, `GET`/`DELETE /v1beta/files/{file}` |
| Realtime | `GET /v1/live`, `GET /v1/robotics/stream` |

Management dashboard routes:

| Capability | Route |
| --- | --- |
| Health & Status | `GET /health`, `GET /api/status` |
| Models & Accounts | `GET /api/models`, `GET/POST /api/accounts`, `GET/POST /api/accounts/import/chrome`, `PUT/DELETE /api/accounts/{id}` |
| Login & Refresh | `POST /api/accounts/{id}/login`, `POST /api/accounts/{id}/refresh` |
| Remote Pairing | `POST /api/pairing`, `POST /api/pairing/accounts` |
| Service Controls | `POST /api/control/start`, `POST /api/control/stop` |
| Config | `GET /api/config` (read-only inspector) |
| Cooldowns & Requests | `GET /api/cooldowns`, `GET /api/requests`, `POST /api/requests/{id}/cancel` |
| Usage & Bodies | `GET /api/usage`, `GET /api/usage/records`, `GET /api/usage/records.csv`, `GET /api/requests/{id}/body` |
| Logs & Events | `DELETE /api/logs`, `GET /api/events` |
| Updates | `GET /api/update`, `POST /api/update` |

Adding upstream capabilities starts in `internal/aistudio`: encoding actual array slots, decoding server events, and projecting them to public protocols in `internal/api`. Model methods, contexts, output limits, tools, voices, image specifications, and video formats all derive from real-time `ListModels`.

`runtimeManager` implements the foundational `Service` interface as well as Video, File, Bidi, and Transcription extension interfaces. `internal/app` handles resource-account bindings, cross-account file replication, runtime state, and retry workflows; `internal/api` manages HTTP, SSE, and WebSocket DTOs (the request, response, and event objects used by public protocols). For full request fields, response DTOs, SSE, and WebSocket events, see [protocol.md](protocol.md).

Frontend development commands:

```powershell
cd web
npm run dev
npm run typecheck
npm run lint
npm run format:check
npm run build
```

Vite writes production assets to `internal/webui/dist`. The management dashboard communicates via local `/api` routes to manage generation services, logs, accounts, configuration, model cooldowns, active requests, and SSE status events; authentication state and WAA objects are never stored in browser storage.

The admin dashboard provides Simplified Chinese, Traditional Chinese, English, Japanese, Korean, French, and German, stored in browser `user_lang`. Strings reside in `web/src/locales/<locale>.ts`; `zh-CN.ts` defines all keys, and other languages provide matching `messages` and `logs`. Management error codes map to `error.<code>`, substituting `{detail}` with `error.message`. Service logs are structured as "stage | key=value", with stage names, keys, and values mapped per the `logs` table in the selected language.

`internal/webui/embed.go` uses `//go:embed dist`, requiring built frontend assets before building Go binaries. The dashboard receives `status`, `models`, `accounts`, `log`, `cooldowns`, and `request` events from `/api/events`.

The API playground consumes SSE via `eventsource-parser`, completing requests on respective protocol finish events; mid-stream errors and premature disconnections are displayed as failures. Flushing errors in response headers, bodies, and heartbeats propagate along the HTTP write path, and event forwarding releases account leases upon cancellation. The playground executes one request at a time, and switching tabs does not interrupt ongoing requests.

## 5. Protocol Implementation Workflow

The logs panel retains the latest 2000 events, displaying the latest 200 rows after merging by request and execution stage. When autoscroll is disabled, the current top row remains pinned while new logs append at the bottom. Request details and stage steps render upon first expansion.

New upstream capabilities are implemented in the following order:

1. Define canonical request, response types, and model capabilities in `internal/aistudio`
2. Encode corresponding JSON+protobuf arrays documented in [protocol.md](protocol.md)
3. Decode network increments into canonical events
4. Project into OpenAI, Responses, Anthropic, and Gemini protocols in `internal/api`
5. Bind structured state and management controls to the dashboard

`internal/api` consumes canonical requests and events, while `internal/aistudio` manages account files, WAA runtimes, raw upstream arrays, and real-time model capabilities. Resource-based operations record the creating account ID, routing subsequent polling, downloads, and prompt references to that account.

1. Define canonical request, response types, and model capabilities in `internal/aistudio`
2. Encode corresponding JSON+protobuf wire arrays described in [protocol.md](protocol.md)
3. Decode network stream deltas into canonical events
4. Project events into OpenAI, Responses, Anthropic, and Gemini protocols in `internal/api`, matching chunking, fields, and ordering of official protocol outputs
5. Bind structured states and operational controls into the admin console

Recognized fields validate types and oneof constraints. Unknown non-empty slots are preserved as provider events or raw extension fields; uninterpretable consumed fields, missing finish frames, and invalid media payloads return structured protocol errors.

## 6. Build and Contribution

Build frontend assets before compiling the release binary:

```powershell
cd web
npm ci
npm run build
cd ..
go build -trimpath -o aistudio2api.exe ./cmd/aistudio2api
```

Run functional acceptance checks, frontend verification, and Go static analysis prior to submission:

```powershell
go vet ./...
```

Windows executable icon resources are located at `cmd/aistudio2api/rsrc_windows_amd64.syso`, generated from icons in `cmd/aistudio2api/winres/` via `go-winres make --in winres/winres.json --arch amd64` and embedded automatically during `go build` on Windows amd64. Windows release archives include `aistudio2api.exe` and `start.bat`; other platforms use the same Go binary. Camoufox is prepared automatically on first startup; when `WAA_BACKEND=go`, it is prepared on first browser login. Contributions should focus on single features or protocol updates, using sanitized request and response examples.

GitHub Actions runs frontend linting, typechecking, building, and minimum-Go-version checks on `main` commits and Pull Requests. Release packages are compiled with current stable Go across Windows amd64, Linux amd64/arm64, and macOS amd64/arm64. Pushing a `v*` tag triggers automated Release creation with attached binaries, startup scripts, example configs, and documentation; tags containing `-` are published as pre-releases. Linux/amd64 and linux/arm64 container images for the same tag are pushed to `ghcr.io/mag1cfall/aistudio2api`, and official releases also update `latest`. Regular build artifacts are retained for 7 days in Actions, while Release assets are preserved indefinitely.

Online updates on the admin dashboard "Version" page are implemented in `internal/app/update.go`:

| Step | Behavior |
| --- | --- |
| Check Version | `GET /api/update` queries `https://api.github.com/repos/Mag1cFall/AIStudio2API/releases/latest`, cached for 1 hour (`?refresh=1` forces immediate re-check); requests use the first proxy in `PROXY`, or system proxy when unset |
| Update Method | `binary` when running official release version; `development` for pseudo-versions, `(devel)`, and `+dirty` builds; `container` when `/.dockerenv` or `/run/.containerenv` exists |
| Download & Verify | `POST /api/update` runs only for `binary` when a newer version exists; downloads `aistudio2api-<version>-<os>-<arch>.zip` (Windows) or `.tar.gz`, verifies SHA-256 against release `digest`, and extracts binary |
| Replace | `minio/selfupdate` replaces current binary with rollback on error; on Windows, original binary is renamed to `.aistudio2api.exe.old` |
| Restart | After returning 202, terminates HTTP server, generation service, and Camoufox in SIGTERM order (HTTP server waits up to 10s for active connections before disconnecting), then relaunches with original arguments plus `-open-ui=false` |

`POST /api/update` returns 409 for `development`, `container`, or already-up-to-date instances. The UI polls `/api/status` every second, reloading on version change or 401.

Source contributions include protocol implementations, frontend source code, and public documentation. Local account states, cookies, tokens, proofs, prompt bodies, response bodies, and runtime artifacts must remain local.
