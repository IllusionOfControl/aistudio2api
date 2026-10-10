# Google AI Studio Private Protocol

This document defines the Google AI Studio private protocol, authentication states, WAA runtime, JSON+protobuf arrays, incremental events, tools, and media chains used by AIStudio2API. Model methods, limits, and capabilities are returned in real time by the account's `ListModels`, and public APIs project raw structures into canonical events and compatible responses.

## 1. Protocol Scope, Endpoints, and Common Headers

| Purpose | Endpoint | Format |
| --- | --- | --- |
| Page origin | `https://aistudio.google.com` | HTTPS |
| MakerSuite RPC | `https://alkalimakersuite-pa.clients6.google.com/$rpc/google.internal.alkali.applications.makersuite.v1.MakerSuiteService/<METHOD>` | `application/json+protobuf` |
| WAA RPC | `https://waa-pa.clients6.google.com/$rpc/google.internal.waa.v1.Waa/<METHOD>` | `application/json+protobuf` |
| BotGuard interpreter | `https://www.google.com/js/bg/<INTERPRETER_HASH>.js` | JavaScript |
| Drive upload | `https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id` | `multipart/related` |
| Drive resumable upload | `https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&fields=id` | Chunked HTTPS body |
| Drive download | `https://www.googleapis.com/drive/v3/files/<FILE_ID>?alt=media` | HTTPS body |

MakerSuite requests use the following common headers:

| Header | Source |
| --- | --- |
| `content-type` | Fixed to `application/json+protobuf` |
| `user-agent` | Firefox UA with account-pinned fingerprint |
| `x-user-agent` | Official web gRPC-Web identifier |
| `x-goog-api-key` | Dynamic value from AI Studio landing page or active official web requests |
| `x-goog-authuser` | Active account official web request |
| `x-aistudio-visit-id` | Landing page initialization or active official web request |
| `x-aistudio-g1-tier` | `GetAiStudioBenefitTier` return value mapped to `TIER0`, `TIER1`, or `TIER2` |
| `x-goog-ext-519733851-bin` | Dynamic value from active official web requests; encoded by `GetLoggingContext` in pure Go WAA backend |
| `authorization` | Three-part SAPISID signature |
| `cookie` | Cookies visible to the target RPC for the current account |
| `origin`, `referer` | `https://aistudio.google.com` |
| `accept-language` | Account locale |

The `x-goog-api-key` header is a dynamic public value used by the AI Studio web page, distinct from user-created Google Cloud API keys; the free web pipeline still relies on Cookies, SAPISID signatures, and WAA proofs.

WAA-protected `GenerateContent` requests are dispatched via account-pinned Camoufox browser pages to preserve native Firefox TLS, HTTP/2, headers, cookies, and page fingerprints; other MakerSuite and Drive requests use a Go HTTP transport over the same account-pinned egress. When `WAA_BACKEND=go`, protected requests are dispatched by the service process matching a Firefox 152 network profile through the same pinned egress; see [WAA Implementation](waa.md).

JSON+protobuf represents protobuf messages using arrays. Array indices are 0-based while protobuf fields are 1-based, meaning field `N` corresponds to index `N-1`. Google responses allow omitting empty slots, resulting in `[,value]`; the decoder first normalizes omitted slots to `null` before extracting repeated messages from the full JSON root value. HTTPS chunks provide only raw byte sequences, with business event boundaries determined by the array structure.

The protocol core uses the following MakerSuite RPCs:

| RPC | Purpose |
| --- | --- |
| `ListModels` | Fetch models, methods, limits, default parameters, and capability options |
| `CountTokens` | Authoritative input token counting |
| `GenerateContent` | Text, thinking, function calling, Google tools, images, audio, and music |
| `GenerateAccessToken` | Retrieve Drive bearer token |
| `GenerateVideo` | Create Veo long-running task |
| `GetGenerateVideoOperation` | Poll Veo long-running task |

AI Studio page initialization also involves the following control plane RPCs:

| RPC | Purpose |
| --- | --- |
| `GetLoggingContext` | Page logging context |
| `GetUserPreferences` | User preferences and onboarding status |
| `UpdateUserPreferences` | Update user preferences such as onboarding status |
| `ListPromos` | Page promotional campaigns |
| `GetAiStudioBenefitTier` | Account tier enumeration and tier header |
| `ListRecentApplets` | Recent Applets |
| `ListPrompts` | Prompt catalog |
| `GetUserRestrictions` | Account restrictions |

Upon startup, the management process loads accounts and prepares common headers. `POST /api/control/start` refreshes the live model catalog, warms up WAA workers, and enables the data plane, after which business capabilities invoke corresponding RPCs on demand.

In this document, "data plane" refers to the generation service that handles public API requests and can be toggled via Stop/Start.

## 2. SAPISID, Chrome DBSC, Cookies, and Account State

### SAPISID Authorization

The `authorization` header is signed separately by three cookies:

| Token Label | Cookie |
| --- | --- |
| `SAPISIDHASH` | `SAPISID` |
| `SAPISID1PHASH` | `__Secure-1PAPISID` |
| `SAPISID3PHASH` | `__Secure-3PAPISID` |

All three segments use the same Unix timestamp in seconds:

```text
source = "<TIMESTAMP> <COOKIE_VALUE> https://aistudio.google.com"
digest = lowercase_hex(SHA1(source))
token = "<LABEL> <TIMESTAMP>_<DIGEST>"
authorization = token_1 + " " + token_2 + " " + token_3
```

When response headers arrive, `Set-Cookie` directives from MakerSuite responses are sequentially merged with the account's latest `storage-state.json` and atomically written back. Signing, cookie selection, and expiration checks are all evaluated against the account state freshly re-read at request time.

### Windows Chrome DBSC Import

Windows Chrome import recovers OAuth credentials and Device Bound Session Credentials from the profile:

```text
Chrome Local State + Profile Preferences + Web Data/token_service
  -> Gaia ID, v20 refresh token ciphertext, wrapped binding key
  -> Decrypt App-Bound v20 master key
  -> AES-256-GCM decrypt refresh token
  -> Obtain DBSC challenge via OAuthMultilogin sentinel request
  -> Issue ES256 assertion using NCrypt device key
  -> Decrypt server cookies via X25519/HPKE
  -> Save Playwright storage state structure and renewal credentials
```

`Local State.os_crypt.app_bound_encrypted_key` is Base64-encoded with an `APPB` prefix. The application loads an embedded ABE helper into an isolated, hidden Chrome process to retrieve the 32-byte master key; the transient process tree is managed by a Windows Job Object. `token_service.encrypted_token` uses the format `v20 || nonce[12] || ciphertext+tag`, which is decrypted with AES-GCM using this master key.

OAuthMultilogin uses the `MultiOAuth` header. The first assertion is `DBSC_CHALLENGE_IF_REQUIRED`, and the response provides a challenge; the JWT header of the second assertion uses `ES256` and `DEVICE_BOUND_SESSION_CREDENTIALS_ASSERTION`. The payload binds the Google OAuth client, challenge, device public key issuer, and ephemeral HPKE public key. Cookie ciphertexts are decrypted using X25519, HKDF-SHA256, and AES-128-GCM.

The Chrome import state preserves the source, Gaia ID, refresh token, and wrapped binding key within the `aistudio2api` extension of `storage-state.json`. Login page redirects, expired signature cookies, HTTP `401`, and protocol Code 16 enter the same authentication recovery workflow, covering worker pre-warming, on-demand startup, standard RPCs, protected RPCs, and Live connection establishment. The service preferentially renews cookies on the same account egress; if saved OAuth renewal material is rejected, credentials for the same Google account email and Gaia ID are imported from the local Chrome profile. Accounts lacking the `aistudio2api` extension can also acquire materials and transition to renewal from local Chrome when signed in to the same email. Accounts requiring user interaction, invalid refresh tokens, and authentication renewal failures mark the account as `auth_required`.

`storage-state.json` preserves Playwright root fields and unknown extension fields; its defined schema is shown below. `wrapped_binding_key` is a Base64-encoded JSON string representing a Go `[]byte`.

```json
{
  "cookies": [
    {
      "name": "<NAME>",
      "value": "<VALUE>",
      "domain": ".example.com",
      "path": "/",
      "expires": -1,
      "httpOnly": true,
      "secure": true,
      "sameSite": "Lax",
      "partitionKey": "<OPTIONAL>"
    }
  ],
  "origins": [
    {
      "origin": "https://example.com",
      "localStorage": [{"name": "<NAME>", "value": "<VALUE>"}]
    }
  ],
  "aistudio2api": {
    "source": {"browser": "chrome", "profile": "<PROFILE>", "email": "<EMAIL>"},
    "oauth": {
      "gaia_id": "<GAIA_ID>",
      "refresh_token": "<REFRESH_TOKEN>",
      "wrapped_binding_key": "<BASE64>"
    }
  }
}
```

Cookie attributes `name`, `value`, `domain`, `path`, `expires`, `httpOnly`, `secure`, `sameSite`, and the optional `partitionKey` are persisted verbatim; `sameSite` accepts an empty value, `Lax`, `Strict`, or `None`. The `origin` must contain both scheme and host. Request cookies filter out expired entries and mismatched Secure/domain/path entries; cookies with the same name are sent in descending order of path length. `Set-Cookie` directives from standard HTTP responses replace or delete unpartitioned entries matching the same name, domain, and path, while partitioned entries with the same name are retained independently. During browser restoration, `partitionKey` maps to BiDi `storageKey.sourceOrigin`; empty values fall back to the default partition.

### Account Persistent State

| File | Description |
| --- | --- |
| `auth/<Google Email>/account.json` | Email, enabled, proxy, locale, timezone |
| `auth/<Google Email>/storage-state.json` | Cookies, localStorage, and optional Chrome renewal credentials |
| `auth/<Google Email>/camoufox-fingerprint.json` | Account-pinned navigator, screen, fonts, language, locale, and timezone configuration |
| `auth/<Google Email>/runtime-state.json` | Account benefit tier, verified model eligibility, cooldowns, and Drive/Veo resource bindings |
| `auth/<Google Email>/camoufox-cache/` | HTTP disk cache for this account's Camoufox instance, held exclusively by running WAA Workers |
| `auth/.leases/<Google Email>.lock` | Cross-process exclusivity lock for the same account directory |
| `auth/.leases/<Google Email>.runtime.lock` | Short-transaction lock for each `runtime-state.json` read, merge, and writeback cycle |
| `[User Cache]/AIStudio2API/runtime-leases/<Google Email>.lock` | WAA Worker reservation lock for this email on the local machine |

The lowercase Google email address serves as the account directory name, admin dashboard identifier, and logging source. The locale and timezone for new accounts are read from the host machine's settings; the admin dashboard uses browser language and IANA timezone, while the CLI uses operating system language and timezone. Initialization, WAA, MakerSuite, OAuth renewal, and Drive all route through the account's pinned proxy. The locale simultaneously configures navigator language, Accept-Language, and region; the timezone configures the browser timezone. Re-logins and WAA runtimes reuse the same account fingerprint. Multiple processes on the same machine share WAA runtime leases by email; the scheduler only spawns Workers for unoccupied emails.

The root fields of `runtime-state.json` are `cooldowns`, `resources`, `model_access`, `benefit_tier`, and `catalog_fingerprint`. Cooldown values follow `{until,reason?}`; model access values follow `{state,checked_at,reason?}`; resource values follow `{kind?,name?,mime?,size?,purpose?,created_at,video?:{model,seconds,size}}`.

Each runtime state transaction first acquires `auth/.leases/<Account>.runtime.lock`, retrying every 25ms with a maximum wait time of 2 seconds; transactions carrying a caller context (Go cancellation context) terminate immediately if the caller cancels or an earlier deadline is reached. After acquiring the lock, current on-disk values are re-read, only target fields are merged, and `runtime-state.json` is atomically replaced via a temporary file before synchronizing in-memory state and resource ownership. Lock acquisition failures, read failures, writeback failures, and unlock failures all return the original error chain.

The scheduler selects candidates based on real-time model methods, capability, AccessModes, and account benefit tiers. It prioritizes warmed accounts that have already successfully invoked the target scope (the state-tracking scope for model eligibility and cooldowns), then sorts them by the target model's most recent time-to-first-event. Resident workers preferentially cover accounts capable of calling more models. When warmed accounts fall below the limit, standby accounts are promoted; when warmed accounts are busy, requests wait for concurrency slots. Same-account WAA proofs are generated serially, while prepared MakerSuite HTTP requests execute concurrently; the first active request acquires `.leases/<email>.lock`, and the last releases it. Drive files, Veo operations, and artifact files always use their creating account.

### Account Benefit Tiers

The `GetAiStudioBenefitTier` request payload is `[]`, and the enum mapping for response field 1 is as follows. `[]`, `[null]`, and `[0]` all represent Free; if subsequent fields exist in the response, the tier is still determined by field 1.

| Value | Benefit Tier | RPC Header |
| ---: | --- | --- |
| 0 | Free | None |
| 1 | Pro | `X-AIStudio-G1-Tier: TIER1` |
| 2 | Ultra | `X-AIStudio-G1-Tier: TIER2` |
| 3 | Plus | `X-AIStudio-G1-Tier: TIER0` |

The official web console injects this header for `GenerateContent`, `CountTokens`, Interaction, Code Assistant, and Veo RPCs. Model field 83 specifies the access mode: `1` denotes paid API key, `3` denotes Pro/Ultra subscription, and `4` denotes Ultra subscription. The public model catalog merges real-time `ListModels` records across all accounts; account scheduling selects specific accounts using AccessModes, BenefitTier, and successful invocation history.

## 3. WAA Challenge, Official VM, and Fresh Proof

Protected requests use the following pipeline:

```text
Waa/Create
  -> decode challenge
  -> load interpreter by current hash
  -> initialize official VM lifecycle
  -> SHA-256(binding prompt) as lowercase hex
  -> snapshot({TYb:{content:<DIGEST>}})
  -> write fresh proof into request
  -> send protected MakerSuite RPC
```

When `WAA_BACKEND=camoufox`, the official VM runs in a Camoufox page with a fixed account fingerprint, and protected requests are sent via the page's native `fetch`. When `WAA_BACKEND=go`, the VM runs in-process inside `goja` with a Firefox-shaped host environment, and protected requests are sent via Go HTTP matching the Firefox 152 network profile. For details on bootstrapping, challenge decoding, interpreters, initialization parameters, host environments, lifecycles, failure handling, and troubleshooting upstream changes, see [WAA Implementation](waa.md).

The `Waa/Create` request is a JSON+protobuf array. When a worker starts up, it only includes the request key; when the VM refreshes, it appends the current interpreter hash and the unbound snapshot from the previous VM:

```json
["lmnUSbltwc5ULv48iKLX"]
["lmnUSbltwc5ULv48iKLX", "<INTERPRETER_HASH>", "<PREVIOUS_SNAPSHOT>"]
```

Outer index `1` of the response contains the Base64 challenge. After decoding, each byte is incremented by `97` (+97), yielding the message ID, interpreter, program, global function name, and client experiments state. The interpreter is cached by hash, where the digest is unpadded Base64URL-encoded SHA-256. The program is bound to the current `Create` lifecycle, and the proof binds the current prompt digest to the VM's internal state, generating a fresh proof for each request.

The underlying input to the snapshot is a 4-slot array, with the first slot carrying the binding:

```javascript
[{content: sha256(bindingPrompt)}, undefined, undefined, undefined]
```

The return value is a proof string prefixed with `!`. For `GenerateContent` and `CreateInteractionStream`, it is written to field 5; for the Build proxy, field 3; for `GenerateVideo`, field 8; and for each Bidi client wire, field 6. All other slots of the original request remain unchanged. For the binding prompt specifications of each RPC, see [WAA Implementation](waa.md). In `GenerateContent`, prompts are concatenated using a single space in the original order of `contents` and `parts`.

`Waa/Ping` request and successful response:

```json
["lmnUSbltwc5ULv48iKLX", "<BOTGUARD_RESPONSE>"]
[]
```

Field 1 is `request_key`, and field 2 is `botguard_response`. A valid proof, a corrupted proof, an omitted field 2, an incorrect request key, or even missing account authentication can all return HTTP 200 and `[]`. A successful Ping only indicates that the WAA RPC endpoint, API consumer identity, and field types are reachable. The status of the Worker VM, snapshot proof, binding, account session, model eligibility, and `GenerateContent` acceptance are determined solely by the actual protected business RPC.

Upon startup, the generation service warms up account WAA Workers according to the configured resident count and startup concurrency. A single account Worker provides proofs for all standard generation models under that account, and switching business models directly reuses the active Worker. Snapshot executions for the same account are serialized.

## 4. ListModels, CountTokens, and GenerateContent Requests

### ListModels

Request body:

```json
[]
```

The root response shape is `[[<MODEL_ROW>, ...]]`. The model list is located at field 1; subsequent root fields do not alter the model catalog. Model row fields:

| JSON Index | Protobuf Field | Content |
| ---: | ---: | --- |
| 0 | 1 | `models/<MODEL_ID>` |
| 2 | 3 | Version |
| 3 | 4 | Display name |
| 4 | 5 | Description |
| 5 | 6 | Input token limit |
| 6 | 7 | Output token limit |
| 7 | 8 | Supported methods |
| 8 | 9 | Default temperature |
| 9 | 10 | Default topP |
| 10 | 11 | Default topK |
| 56 | 57 | Model aliases |
| 64 | 65 | Primary capability codes |
| 66 | 67 | TTS voice list |
| 70 | 71 | Veo configuration |
| 71 | 72 | Thinking default configuration |
| 74 | 75 | Secondary capability codes |
| 75 | 76 | Image aspect ratio codes |
| 76 | 77 | Image output resolution codes |
| 77 | 78 | Paid flag; displays Paid when value is `2` |
| 78 | 79 | Interaction configuration: index 2 is background task, index 3 type `1` is agent, `2` is model |
| 82 | 83 | Model access method |

Capability code mapping:

| Code | Capability | Code | Capability |
| ---: | --- | ---: | --- |
| 1 | chat model | 9 | code execution |
| 10 | function declarations | 12 | Google Search |
| 13 | URL Context | 20 | Veo route |
| 21 | image route | 25 | thinking |
| 26 | live route | 35 | thinking budget |
| 37 | speech route | 43 | media resolution |
| 47 | aspect ratio | 49 | output resolution |
| 52 | thinking level | 53 | music route |
| 54 | image search | 58 | Google Maps |
| 59 | private Interaction route | 85 | TTS segmented speaker |
| 46 | Live real-time translation | | |

Unknown capability codes are preserved as raw values using `capability_code_<N>` or `secondary_capability_code_<N>`.

The public model object adds the following semantic keys for known primary capability codes:

| Code | Capability Key | Code | Capability Key |
| ---: | --- | ---: | --- |
| 1 | `chat_model` | 9 | `code_execution` |
| 10 | `function_declarations` | 12 | `google_search` |
| 13 | `browse` | 20 | `video_route` |
| 21 | `image_route` | 25 | `thinking` |
| 26 | `live_route` | 35 | `thinking_budget` |
| 37 | `speech_route` | 43 | `media_resolution` |
| 47 | `aspect_ratio` | 49 | `output_resolution` |
| 52 | `thinking_level` | 53 | `music_route` |
| 54 | `image_search` | 58 | `google_maps` |
| 59 | `interaction_route` | 74 | `transcription_word_timestamps` |
| 76 | `transcription_language_codes` | 77 | `transcription_output` |
| 80 | `transcription_speaker_labels` | 81 | `transcription_custom_vocabulary` |
| 84 | `transcription_smart` | 85 | `speech_metadata` |
| 46 | `speech_translation` | | |

Each primary capability code is retained as `capability_code_<N>`, while adding semantic keys for known codes in the table above; secondary capability codes are retained as `secondary_capability_code_<N>`.

Image and video options use enum codes:

| Type | Code Value Mapping |
| --- | --- |
| Image/video aspect ratio | `1=1:1`, `2=9:16`, `3=16:9`, `4=3:4`, `5=4:3`, `6=3:2`, `7=2:3`, `8=5:4`, `9=4:5`, `10=21:9`, `11=9:21`, `12=1:4`, `13=4:1`, `14=1:8`, `15=8:1` |
| Image resolution | `1=1K`, `2=2K`, `3=4K`, `4=512` |
| Video duration | `1=5s`, `2=6s`, `3=7s`, `4=8s`, `5=4s` |
| Video resolution | `1=720p`, `2=1080p`, `3=4k`, `4=368p`, `5=360p` |

Veo field 71 aspect ratio, duration, and resolution are located at sub-indices `4`, `5`, and `9`, respectively; when sub-index `7` contains `3` and sub-index `9` is empty, resolutions default to official values `720p`, `1080p`, and `4k`. TTS field 67 is a repeated voice row, where index `0` of each row is the voice name. Thinking field 72's default level is located at sub-index `5`.

Field 57 alias can be `["models/<ALIAS>"]` or a repeated row; when shaped as rows, take index `0` of each row and remove the `models/` prefix. The complete set of public `capability_options` keys consists of `aliases`, `voices`, `image_aspect_ratios`, `image_output_resolutions`, `video_aspect_ratios`, `video_durations_seconds`, and `video_output_resolutions`; keys without values are omitted.

### CountTokens

Plain text without system:

```json
["models/<MODEL_ID>", [<CONTENT>, ...]]
```

With system, inline data, external media, or Drive file:

```json
["models/<MODEL_ID>", null, ["models/<MODEL_ID>", [<CONTENT>, ...], null, null, null, <SYSTEM>]]
```

Request shape selection:

| Condition | Root Structure | GenerateContent Sub-message Position |
| --- | --- | --- |
| Plain text contents | `[model, contents]` | — |
| system instruction | `[model, null, generate]` | `$[2][5]` |
| function / Google tools | `[model, null, generate]` | `$[2][6]` |
| inline data, external media, Drive, function call/result, code result | `[model, null, generate]` | `$[2][1]` |

Complete token count request including system and function declarations:

```json
[
  "models/gemini-3.6-flash",
  null,
  [
    "models/gemini-3.6-flash",
    [
      [
        [[null, "Call ping to check service"]],
        "user"
      ]
    ],
    null,
    null,
    null,
    [
      [[null, "You are a diagnostic assistant"]],
      "user"
    ],
    [
      [null, [["ping", "Check service"]]]
    ]
  ]
]
```

The response is a single-element array:

```text
[<INPUT_TOKEN_COUNT>]
```

Index `0` is the authoritative input token count. Other slots are preserved as opaque protocol fields.

### Content, Part, and system

Content shape:

```json
[[<PART>, ...], "user|model"]
```

Model completion frames with a finish reason can use `[null,"model"]`, which provides only the termination status and usage.

Client tool results use the `user` role. Part fields:

| JSON Index | Protobuf Field | Content |
| ---: | ---: | --- |
| 1 | 2 | text |
| 2 | 3 | inline data `[mime, base64]` |
| 5 | 6 | Drive file `[fileId]` |
| 6 | 7 | external media `[mime, url]` |
| 7 | 8 | executable code `[languageCode, code]` |
| 8 | 9 | code execution result `[outcomeCode, output]` |
| 10 | 11 | function call `[name, Struct, callId?]` |
| 11 | 12 | function result `[name, Struct, callId?]` |
| 12 | 13 | thought boolean |
| 14 | 15 | thought signature |
| 22 | 23 | transcription metadata `[text, speaker?, timestamp spans?]` |

system instruction:

```json
[[[null, "<SYSTEM_TEXT>"]], "user"]
```

### GenerateContent

Root message fields:

| JSON Index | Protobuf Field | Content |
| ---: | ---: | --- |
| 0 | 1 | `models/<MODEL_ID>` |
| 1 | 2 | contents |
| 2 | 3 | safety settings |
| 3 | 4 | generation config |
| 4 | 5 | fresh WAA proof |
| 5 | 6 | system instruction |
| 6 | 7 | tools |
| 10 | 11 | Fixed value `1` |
| 13 | 14 | `[[null,null,<TIMEZONE>]]` |
| 14 | 15 | User Cloud API key; remains `null` for free web chains |

safety settings:

```json
[
  [null, null, 7, 5],
  [null, null, 8, 5],
  [null, null, 9, 5],
  [null, null, 10, 5]
]
```

Each entry is `[null, null, category, threshold]`, matching Gemini API enum numbering: category `HARM_CATEGORY_HARASSMENT`=7, `HATE_SPEECH`=8, `SEXUALLY_EXPLICIT`=9, `DANGEROUS_CONTENT`=10, `CIVIC_INTEGRITY`=11; threshold `BLOCK_LOW_AND_ABOVE`=1, `BLOCK_MEDIUM_AND_ABOVE`=2, `BLOCK_ONLY_HIGH`=3, `BLOCK_NONE`=4, `OFF`=5. Non-image models default to sending categories 7-10 above as `OFF`, overridden or appended by categories in the Gemini request; image models send only categories present in the request (null if empty). Upstream returns HTTP 400 parameter errors for `HARM_CATEGORY_CIVIC_INTEGRITY` with `OFF`; other categories accept `OFF`.

generation config fields:

| JSON Index | Protobuf Field | Content |
| ---: | ---: | --- |
| 1 | 2 | stop sequences |
| 3 | 4 | max output tokens |
| 4 | 5 | temperature |
| 5 | 6 | topP |
| 6 | 7 | topK |
| 7 | 8 | response MIME type |
| 8 | 9 | response schema |
| 13 | 14 | Fixed value `1` |
| 14 | 15 | response modalities: TEXT=`1`, IMAGE=`2`, AUDIO=`3` |
| 15 | 16 | speech config |
| 16 | 17 | thinking config `[1, budget?, null, level]` |
| 17 | 18 | media resolution：LOW=`1`、MEDIUM=`2`、HIGH=`3` |
| 18 | 19 | seed |
| 26 | 27 | image config `[aspectRatio?, imageSize?]` |
| 31 | 32 | transcription config |

Generation parameter validation:

| Parameter | Default Source | Valid Values |
| --- | --- | --- |
| max output | ListModels field 7 | `1..model.outputTokenLimit` |
| temperature | ListModels field 9 | `0..2` |
| topP | ListModels field 10 | `0..1` |
| topK | ListModels field 11 | Non-negative integer |
| thinking level | ListModels field 72 | Low=`1`, Medium=`2`, High=`3`, Minimal=`4` |
| thinking budget | Request value | Model capability codes include thinking budget |

- If max output tokens exceeds the model limit, it is clamped to the model limit

`reasoning_effort` / `thinkingLevel` / Anthropic `output_config.effort` accepts `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`; `xhigh` and `max` are treated as `high`, and `THINKING_LEVEL_UNSPECIFIED` is treated as unset. `none` uses a budget of 0 on models that only support thinking budget, the lowest available level on models supporting thinking level, and is ignored on models without thinking support. When a model only supports thinking budget, an explicit effort must also provide a numeric thinking budget; when a model supports thinking level, a budget converts to thinking level according to compatibility tiers (1024, 8192, 24576). Model requests lacking thinking support omit thinking configuration.

response modalities:

`wire` refers to the raw array field sent upstream.

| Output | wire | Default Route |
| --- | --- | --- |
| text | `[1]` | chat |
| image | `[2]` | image route |
| image + text | `[2,1]` | Explicit combined request |
| audio | `[3]` | speech / music route |

AUDIO uses an independent output modality, intersecting with model capabilities when requested alongside other modalities. JSON Schema type codes are string=`1`, number=`2`, integer=`3`, boolean=`4`, array=`5`, object=`6`; schema supports format, description, nullable, enum, items, properties, required, and field 23 `propertyOrdering`.

The following minimal combined request includes system, text, function declarations, generation config, WAA proof, and account timezone. Consecutive null slots are kept on the same line; see the tables above for field definitions:

```json
[
  "models/gemini-3.6-flash",
  [
    [
      [[null, "Call ping to check service"]],
      "user"
    ]
  ],
  [
    [null, null, 7, 5],
    [null, null, 8, 5],
    [null, null, 9, 5],
    [null, null, 10, 5]
  ],
  [null, null, null, 512, 0.2, 0.95, 40, null, null, null, null, null, null, 1],
  "!WAA_PROOF",
  [
    [[null, "You are a diagnostic assistant"]],
    "user"
  ],
  [
    [null, [["ping", "Check service"]]]
  ],
  null,
  null,
  null,
  1,
  null,
  null,
  [[null, null, "Asia/Taipei"]]
]
```

## 5. Incremental Streaming, Thinking, Usage, Sources, and Errors

`GenerateContent` returns an incrementally growing JSON+protobuf root array, where root index `0` represents repeated frames. The frame structure is as follows:

| Path | Content |
| --- | --- |
| `$[0][frame][0]` | candidates |
| `$[0][frame][0][0][0]` | candidate content |
| `$[0][frame][0][0][1]` | finish reason code |
| `$[0][frame][0][0][6]` | citations |
| `$[0][frame][0][0][7]` | grounding metadata |
| `$[0][frame][2]` | usage |
| `$[0][frame][7]` | response ID |
| `$[0][frame][3]` when frame 0 is empty | interaction metadata |

The transfer body is a single JSON root value supplied as bytes via network chunks; the decoder consumes each repeated frame in `$[0]` as soon as a complete frame becomes available. Each content frame contains a single candidate, with the candidate content formatted as `[[parts...], "model"]`. A completion frame may simultaneously carry the final set of Parts, usage, the response ID, and the finish reason; reading terminates once root array parsing is complete.

Text frame extracted from `$[0]`:

```json
[
  [
    [
      [
        [[null, "42"]],
        "model"
      ]
    ]
  ]
]
```

The subsequently arriving completion frame contains `finish=1`, usage, and the response ID:

```json
[
  [[null, 1]],
  null,
  [27, 1, 28, null, null, null, null, 0, null, 0],
  null,
  null,
  null,
  null,
  "response_01"
]
```

Quick Reference for High-Frequency Paths:

| Structure | JSONPath | Content |
| --- | --- | --- |
| GenerateContent | `$[0]` | model |
| GenerateContent | `$[1]` | contents |
| GenerateContent | `$[3]` | generation config |
| GenerateContent | `$[4]` | WAA proof |
| GenerateContent | `$[5]` | system instruction |
| GenerateContent | `$[6]` | tools |
| GenerateContent | `$[13][0][2]` | timezone |
| response root | `$[0][frame]` | repeated frame |
| candidate content | `$[0][frame][0][0][0]` | `[[parts], "model"]` |
| candidate finish | `$[0][frame][0][0][1]` | finish reason code |
| candidate finish message | `$[0][frame][0][0][3]` | finish message |
| Part text | `...parts[part][1]` | text |
| Part inline data | `...parts[part][2]` | `[mime, base64]` |
| Part function call | `...parts[part][10]` | `[name, Struct, callId?]` |
| Part thought | `...parts[part][12]` | boolean |
| Part signature | `...parts[part][14]` | signature |
| frame usage | `$[0][frame][2]` | usage array |
| frame response ID | `$[0][frame][7]` | response ID |

When a text Part has `part[12]=true`, it represents a reasoning summary, whereas standard text represents visible content. An inline image with `part[12]=true` is a draft generated during the image model's reasoning process and is not returned as output media; the final image is returned separately as a standard Part. `part[14]` is the thought signature. Signatures may be attached to text, function calls, or standalone empty Parts, and must be passed back verbatim in the next turn:

| Public Protocol | Signature Input | Signature Output |
| --- | --- | --- |
| OpenAI Chat | `extra_content.google.thought_signature` of the assistant tool call | Extension field of the same name on the tool call |
| OpenAI Responses | `reasoning.encrypted_content` immediately preceding subsequent `function_call` | `encrypted_content` of the reasoning item |
| Anthropic | `signature` of a `thinking` or `redacted_thinking` block | `signature` of the thinking block |
| Gemini | `thoughtSignature` of a data Part or standalone Part | `thoughtSignature` of the Part |

When an assistant tool call in OpenAI Chat lacks `extra_content`, the service backfills the most recently issued signature from the current process based on call ID, function name, and arguments; if no match is found, it writes `skip_thought_signature_validator`.

An Anthropic redacted thinking block carries this same opaque state via `data`; adapters preserve this value on both input and output sides. Streaming responses do not emit signatures that arrive after the text.

The reasoning summary is the summary text returned by the server. The thought signature is passed back verbatim as a protocol state field in the subsequent request.

The protocol core emits events in network order: `text`, `reasoning`, `tool_call`, `executable_code`, `code_execution_result`, `grounding`, `citation`, `media`, `thought_signature`, `usage`, `finish`, and `error`.

### Grounding and Citations

Grounding metadata fields:

| JSON Index | Content |
| ---: | --- |
| 0 | search entry point `[renderedContent?, sdkBlob?]` |
| 1 | grounding chunks |
| 2 | grounding supports |
| 3 | retrieval metadata, with dynamic score at sub-index 1 |
| 4 | web search queries |
| 5 | second repeated web search query slot |
| 6 | Maps widget context token |

Indices `4` and `5` are merged by slot and element order, and deduplicated.

`oneof` indicates that at most one field from the group may be set.

The `oneof` indices `0/1/2` for a grounding chunk represent web, retrieved context, and maps, respectively; internal fields are URI, title, text, and place ID in order. A support is structured as `[segment, chunkIndices, confidenceScores]`, where segment is `[partIndex,startIndex,endIndex,text]`. Entries for candidate citations reside at metadata index 0, with each item's URL at index 2 and title at index 3.

Raw metadata containing web chunk, maps chunk, content support, retrieval score, and search queries:

```json
[
  ["<div>Search results</div>", "SDK_BLOB"],
  [
    [["https://example.com/gemini", "Gemini Guide", "Protocol overview"]],
    [null, null, ["https://maps.google.com/?cid=1", "Google Taipei", "", "ChIJ_demo"]]
  ],
  [
    [[0, 0, 12, "Gemini Guide"], [0], [0.98]]
  ],
  [null, 0.91],
  ["Gemini AI Studio protocol"],
  null,
  "MAPS_WIDGET_CONTEXT_TOKEN"
]
```

For Code Execution, the language codes are `0=LANGUAGE_UNSPECIFIED` and `1=PYTHON`. The execution outcome codes are `0=OUTCOME_UNSPECIFIED`, `1=OUTCOME_OK`, `2=OUTCOME_FAILED`, and `3=OUTCOME_DEADLINE_EXCEEDED`.

### Usage

Completion frame usage:

| Array Index | Semantics | Canonical Field |
| ---: | --- | --- |
| 0 | input tokens | `input_tokens` |
| 1 | visible output tokens | `output_tokens` |
| 2 | total tokens | `total_tokens` |
| 7 | tool tokens | `tool_tokens` |
| 9 | thought tokens | `reasoning_tokens` |

Complete usage is returned directly using upstream values. When the completion frame omits visible output tokens, the service reconstructs this value from the upstream total and the remaining breakdown fields. When complete usage is missing entirely, the built-in Gemini SentencePiece tokenizer calculates observable inputs, tool declarations, reasoning summaries, and actual outputs locally.

Input statistics for OpenAI and Anthropic are computed as input + tool, while output statistics are visible output + reasoning. Gemini projects `promptTokenCount`, `candidatesTokenCount`, `thoughtsTokenCount`, and `totalTokenCount` respectively. Hidden thinking token usage comes from upstream usage field 9; the local fallback tallies the reasoning summary returned by the server.

Anthropic streaming `message_start` writes an initial input estimate, while the final `message_delta` overrides it with the completion usage to provide authoritative input and output counts.

Requests carrying stop sequences execute `CountTokens` in parallel under the same account. When the generated text matches the actual sequence, the protocol core terminates the generation stream and constructs the final usage from the `CountTokens` total input count along with already emitted text, reasoning, and tool statistics; if counting fails, it still returns the `stop_sequence` finish state and omits usage.

### Finish and Errors

| code | reason | code | reason |
| ---: | --- | ---: | --- |
| 0 | unspecified | 1 | stop |
| 2 | max_tokens | 3 | safety |
| 4 | recitation | 5 | other |
| 6 | language | 7 | blocklist |
| 8 | prohibited_content | 9 | spii |
| 10 | malformed_function_call | 11 | image_safety |
| 12 | unexpected_tool_call | 13 | too_many_tool_calls |
| 14 | image_prohibited_content | 15 | image_other |
| 16 | no_image | 17 | image_recitation |
| 18 | missing_thought_signature | 19 | `provider_19` |
| Other integers | `provider_<code>` | | |

generation config, safety settings, tools, and transcription configurations are validated against model catalogs loaded for each account before account selection; accepting by any account passes validation. Streaming requests wait for the first event before writing response headers: parameter errors and upstream errors before the first event return HTTP status and error objects non-streamingly; if no event arrives within 10 seconds, headers are written, and subsequent errors return as in-stream error events per protocol.

The root shape of an error response is `[null,[code,message,...]]`. Rejected input returns `[null,[block_reason,null,message]]` as HTTP 400, including the rejection reason and upstream explanation. The protocol core preserves HTTP status, protocol code, and message; public adapters map these to OpenAI, Anthropic, or Gemini error objects. Chat, Responses, Anthropic Messages, and Gemini GenerateContent output normal text from media models as text results; dedicated image endpoints require image results. HTTP/protocol Code 16 and HTTP 401 trigger automatic token renewal and single retry.

Specific terminal state mappings across protocols are as follows:

| Upstream Terminal State | OpenAI Chat | OpenAI Responses | Anthropic Messages | Gemini GenerateContent |
| --- | --- | --- | --- | --- |
| `stop` with function call | `tool_calls` | `completed`, retaining function call item | `tool_use` | `STOP`, retaining functionCall Part |
| `stop_sequence` | `stop` | `completed` | `stop_sequence` with actual sequence | `STOP` |
| `max_tokens` | `length` | `incomplete/max_output_tokens` | `max_tokens` | `MAX_TOKENS` |
| policy, refusal, missing signature, and other anomalous terminal states | `content_filter` | `incomplete/content_filter` | `refusal` | Corresponding Gemini enum or `OTHER` |

Anomalous terminal states take precedence over tool call terminal states within the same result, while already emitted content, reasoning, tool events, and usage are retained in the response. `provider_*` retains its original value in `provider_finish_reason` within Chat choice, Responses response, Anthropic message, or `message_delta`; Gemini retains the code using `finishMessage`. `provider_19` corresponds to `Content blocked` on the AI Studio UI.

## 6. Functions, Google Tools, Drive, and Media

### Functions and Google Tools

Root field 7 is a repeated Tool:

| Tool | Tool Array Shape |
| --- | --- |
| Function declarations | `[null, [[name, description?, schema?], ...]]` |
| Code Execution | `[[]]` |
| Google Search | `[null,null,null,[null,[searchTypes]]]`, with index 0 of `searchTypes` being `[]` |
| Image Search | Same Search tool, with index 1 of `searchTypes` being `[]` |
| URL Context | 8-slot array, with index 7 being `[]` |
| Google Maps | 11-slot array, with index 10 being `[]` |

Index `3` of the Search tool is `[timeRange?,searchTypes]`. `timeRange` is `[start?,end?]`, where each timestamp value is encoded as `["<UNIX_SECONDS>"]`; indices `0` and `1` of `searchTypes` enable web search and image search, respectively.

Public tool names are normalized before generating the Tool arrays described above:

| AI Studio Tool | OpenAI Chat / Responses | Anthropic | Gemini |
| --- | --- | --- | --- |
| function declarations | `function` | Empty type or `custom` | `functionDeclarations` |
| Google Search | `web_search`, `web_search_preview` | `web_search_*` | `googleSearch`, `googleSearchRetrieval` |
| Image Search | `image_search` | `image_search` | `imageSearch` |
| URL Context | `url_context` | `web_fetch*`, `url_context*` | `urlContext` |
| Code Execution | `code_interpreter` | `code_execution*` | `codeExecution` |
| Google Maps | `google_maps` | `google_maps` | `googleMaps` |

- Tools can be combined across declarations; Google Maps cannot be combined with Code Execution or URL Context
- Tool choices `auto`, `none`, `required`, and named tools map to mode field 1 (`1=AUTO`, `2=ANY/REQUIRED`, `3=NONE`) and allowed function names field 2
- Model capabilities gate tool declarations: models lacking `function_declarations` silently drop function declarations and tool choice; unsupported Google search tools are dropped; unknown or conflicting Google tools return an error

Function JSON Structs use protobuf `Struct`/`Value` arrays: maps are represented as `[[[key,value],...]]`; `Value` oneof indices `0..5` represent null, number, string, bool, Struct, and ListValue, respectively. Object keys are sorted before encoding.

For example, given the following function arguments:

```json
{
  "city": "Taipei",
  "days": 2,
  "metric": true,
  "note": null,
  "units": ["C", "F"]
}
```

The encoded Struct is:

```json
[
  [
    ["city", [null, null, "Taipei"]],
    ["days", [null, 2]],
    ["metric", [null, null, null, true]],
    ["note", [0]],
    [
      "units",
      [
        null,
        null,
        null,
        null,
        null,
        [[[null, null, "C"], [null, null, "F"]]]
      ]
    ]
  ]
]
```

The key slots of a complete function call Part are:

```json
[
  null,
  null,
  null,
  null,
  null,
  null,
  null,
  null,
  null,
  null,
  ["multiply", [[["a", [null, 21]], ["b", [null, 2]]]], "call_01"],
  null,
  null,
  null,
  "!THOUGHT_SIGNATURE"
]
```

Where Part index `10` holds the function call, and index `14` holds the thought signature.

Playground function parameter carrier and structured output schemas use the following protobuf fields:

| JSON Schema | Field | JSON Schema | Field |
| --- | ---: | --- | ---: |
| `type` | 1 | `format` | 2 |
| `description` | 3 | `nullable` | 4 |
| `enum` | 5 | `items` | 6 |
| `properties` | 7 | `required` | 8 |
| `minProperties` | 9 | `maxProperties` | 10 |
| `minimum` | 11 | `maximum` | 12 |
| `minLength` | 13 | `maxLength` | 14 |
| `pattern` | 15 | `example` | 16 |
| `oneOf` | 17 | `anyOf` | 18 |
| `allOf` | 19 | `not` | 20 |
| `maxItems` | 21 | `minItems` | 22 |
| `propertyOrdering` | 23 | | |

Structured output schema normalization rules:

| Input Structure | Encoded Result |
| --- | --- |
| Explicit `null` in optional fields | Remove unset fields; `example: null` preserved as data value, `const` verified per constant rules, property names preserved |
| Empty definition, `null`, `{}`, or `true` for 0-arg function | Object parameter structure |
| Open `{}`, `true`, and array without item definition | Open nodes use `TYPE_UNSPECIFIED`, carrier types are filled for Playground and full schema appended to root description |
| Gemini `type:"TYPE_UNSPECIFIED"` | Treated as typeless open node |
| Non-negative integer constraint written as int64 string or `1.0` | Encoded as integer |
| `items:false` | Empty array constraint `maxItems: 0` |
| `prefixItems` | Converted to schema representation |
| Open object schema `additionalProperties` | Schema mapping with open node properties |
| Union type array (e.g., `["string", "null"]`) | Primary type with `nullable: true` |
| Multiple non-null types in type array | Normalized to `anyOf` schema |
| Disallowed keywords (`patternProperties`, `propertyNames`, `dependencies`, etc.) | Stripped with warning |
| Root-level array schema | Wrapped in container object |
| Numerical `const` in function arguments | Local type conversion preserving strict values |

`auto` leaves invocation decisions to the model, and `none` omits tools. Named functions restrict declarations to a subset; Playground writes invocation requirements into generation instructions and checks actual returns, while Build uses native `functionCallingConfig`. Client tool choice mappings are as follows:

| Public Protocol | Auto and None | Required Call |
| --- | --- | --- |
| OpenAI Chat / Responses | Default, `auto`, `none` | `required`, named function |
| Anthropic | Default, `auto`, `none` | `any`, named `tool` |
| Gemini | Default, `AUTO`, `NONE`, `VALIDATED` | `ANY`, `allowedFunctionNames` |

The function call response Part is `[name, Struct, callId?]`; the next turn's function result uses the same shape and returns thought signature verbatim. When a tool result explicitly provides a function name, that name is preserved; when absent, it correlates with calls in the current turn lacking results by call ID, falling back to the single remaining call's name. Each result corresponds to one call; intermediate assistant text does not break correlation, and a new turn of normal dialogue resets correlation; ambiguities or missing call records return invalid argument errors. Function parameters and results use JSON objects; non-object values are wrapped as `{"result": <VALUE>}` to align with Gemini wire format in Build.

### Drive Uploads and File Parts

```text
GenerateAccessToken ["users/me"]
  -> response ["<BEARER_TOKEN>"]
  -> POST Drive multipart/related
       part 1: {"mimeType":"<MIME>","name":"<NAME>"}
       part 2: raw bytes
  -> {"id":"<FILE_ID>"}
  -> GenerateContent Part field 6 ["<FILE_ID>"]
```

Drive tokens, uploads, and downloads use fixed egress endpoints belonging to the file's owning account. File ID to account bindings are persisted in `runtime-state.json`; generation requests can combine files from different accounts, temporarily copying files from other accounts to the current executing account; copies from failed attempts are cleaned up immediately, while copies from successful attempts are cleaned up upon request completion.

Inline attachments in generation requests preferentially upload to the executing account as Drive file Parts. When `GenerateAccessToken` explicitly returns 401, Code 16, or `OAuth error: unauthorized_client`, the request preserves the original inline data Part and the account remains eligible for normal generation; other token errors fail the request and cooldown Drive operations for 15 minutes.

### Gemini 3.5 Transcribe

`gemini-3.5-transcribe` uses Drive file Parts and GenerateContent generation config field 32. The transcription configuration subfields are as follows:

| protobuf field | Content |
| ---: | --- |
| 5 | Word timestamps, enabled with value `1` |
| 6 | Speaker labels, enabled with value `1` |
| 7 | Repeated custom vocabulary |
| 8 | Repeated language codes |
| 9 | Smart transcription, enabled with value `2` |

Transcription metadata resides in response Part field 23, where subfield 1 is text, 2 is speaker label, and 3 is repeated timestamp span. Each span's field 2 and field 3 are start and end times in seconds and nanos. Public transcription endpoints are described in "Files, Transcribe, and Media".

### Nano, TTS, and Lyria

These three media types reuse `GenerateContent`:

| Route | generation config | Response |
| --- | --- | --- |
| Nano image | modalities `[2]`, image config `[aspectRatio?, imageSize?]` | Part field 3 `[mime, base64]` |
| TTS | modalities `[3]`, speech config | Part field 3 audio chunks |
| Lyria | modalities `[3]` | Part field 3 audio chunks |

Single-voice speech config is `[[[voiceName]]]`. When a TTS model request omits voice, the official default `Zephyr` is used. Multi-speaker speech config is `[null,null,[null,[[speaker,[[voiceName]]],...],mode?]]`, where mode `1` is `VERBATIM` and `2` is `CONVERSATIONAL`. Text Part field 41 is SpeechMetadata `[speaker?, style?]`; TTS models with capability code 85 require each text Part to provide a speaker, and lines omit speaker prefixes.

### Veo

`GenerateVideo` uses an 8-slot array, with the WAA proof located in field 8:

```json
[
  "models/<MODEL_ID>",
  "<PROMPT>",
  [1, "<ASPECT_RATIO>", ["<SECONDS>"], "<RESOLUTION>"],
  ["<IMAGE_MIME>", "<BASE64>"] | null,
  ["<DRIVE_FILE_ID>"] | null,
  null,
  null,
  "<WAA_PROOF>"
]
```

The image source oneof for the starting frame is either an inline image or a Drive file. Field 1 of the creation response is the operation ID. The polling request is `["<OPERATION_ID>"]`; polling response field 1 indicates done, and output Drive file ID is at `$[1][0][0][0]`. Both operation and result file bind to the creating account, downloaded via Drive bearer token. Aspect ratio, duration seconds, and resolution snap to the nearest supported values in live model field 71.

### Omni Interaction

Models with catalog field 79 type `2` that are not background tasks (`gemini-omni-1.1-flash`, `gemini-omni-flash-preview`) do not accept `GenerateContent`. For such models, all four public generation APIs switch to `CreateInteractionStream`. Account selection, Worker, and WAA remain identical to GenerateContent; the WAA proof is located in field 5, and its binding is all transmitted text joined by spaces:

```json
[1, 1, null, <INTERACTION>, "<WAA_PROOF>", 1]
```

| Interaction Index | Content |
| ---: | --- |
| 6 | System instruction |
| 17 | `["models/<MODEL_ID>", [null,null,null,null,null,<THINKING_LEVEL>,1,<MAX_OUTPUT_TOKENS>]]`; config index 24 is `[]` when there is no model output in history |
| 26 | `[[<STEP>...]]`; user steps are `[[<CONTENT>...]]`, model steps are `[null,[<CONTENT>...]]`; text content is `[[<TEXT>]]`, Drive attachment content is `[null×8,["<DRIVE_FILE_ID>"]]` |
| 53 | Video output config `[[[null,null,null,[null,null,null,null,null,1]]]]` |

Thinking level values are MINIMAL=1, LOW=2, MEDIUM=3, HIGH=4, converted from `reasoning_effort` or thinking budget according to model capabilities, defaulting to catalog default. Interaction requests accept user, assistant, and tool messages; tool messages are treated as user messages, adjacent same-role messages merge into one step, historical function calls/results and code executions are rewritten as Gemini API JSON text, and assistant attachments move to the subsequent user step; inline attachments upload to Drive, referenced by file ID which does not participate in binding. Audio streaming outputs 24 kHz 16-bit little-endian mono PCM deltas; non-streaming outputs a complete WAV or requested format.

The response is `[[<EVENT>...], <STATUS>?]`. In the content delta at event index 10, field 1 is the main text, field 5 is video `[1,"<BASE64 MP4>"]`, and field 6 is the thought summary, mapping to text, `video/mp4` media, and reasoning events, respectively; the final interaction status `3` at index 19 yields usage (prompt, completion, thinking, and total tokens reside at usage indices 0, 4, 8, and 9) and a `stop` terminal state, while statuses `4` and `5` return errors. The `google.rpc` status following the event list maps to HTTP status codes by its code (quota code 8 maps to 429); errors occurring before the first text event trigger account rotation and cooldown, while subsequent errors terminate the stream with an in-stream error.

### Build Proxy

The official Build web app calls the MakerSuite proxy RPC via the host page to access the Gemini API, with quota tracked separately from Playground. When `UPSTREAM_CHANNELS` enables `build`, generation requests are encoded into Gemini API JSON on the Build channel:

```json
["/v1beta/models/<MODEL_ID>:streamGenerateContent", "<GEMINI_API_JSON>", "<WAA_PROOF>"]
["/v1beta/models/<MODEL_ID>:generateContent", "<GEMINI_API_JSON>", "<WAA_PROOF>", "POST"]
```

The former targets `ProxyStreamedCall`, while the latter targets `ProxyUnaryCall`. WAA proof resides in field 3, with binding computed as SHA-256 of path and body joined by space. RPC selection, headers, catalogs, eligibility, scheduling, field mappings, response decoding, error mappings, and unsupported scopes are detailed in [Build Channel](build.md). Embedding, Live, and realtime music models absent from Playground route exclusively via Build: embedding calls `:batchEmbedContents` via `ProxyUnaryCall`, and Live and realtime music route through the host page WebChannel.

### Live and Robotics WebSocket

`GET /v1/live` and `GET /v1/robotics/stream` upgrade to WebSocket, where the first client frame is setup; public frames and events are detailed in "Live and Robotics Public Frames". Both endpoints use client-supplied models, selecting accounts by `bidiGenerateContent` in live catalog; models providing `bidiGenerateMusic` in Build catalog (e.g. `lyria-realtime-exp`) serve realtime music sessions. Live and realtime music models absent from Playground route via Build with matching public frames, wire format in [Build Channel](build.md). Upstream connection closes return status codes in 1000-4999 directly; unexpected closes return 1011.

The upstream WebChannel consists of a handshake, forward POSTs, a long-polling backchannel, and termination. The handshake query uses `VER=8`, a random `RID`, `CVER=22`, `X-HTTP-Session-Id=gsessionid`, and `count=0`; the response header returns the gsessionid, and the first control envelope is:

```json
[[0,["c","<SID>","",8]]]
```

Query fields for subsequent forward requests are `VER`, `gsessionid`, `SID`, `RID`, `AID`, `zx`, and `t`; form fields are sequentially `count=<N>`, `ofs=<OFFSET>`, and `req0___data__` through `req<N-1>___data__`. Client messages are queued in arrival order; only one forward POST is in flight at any time. Messages arriving while a request is in flight are batched into the next POST, up to a maximum of 25 messages per POST, consistent with the official web client. `audio` and `image` frames return immediately upon enqueueing, whereas `text`, `tool_response`, and `media_end` wait for the ACK of their containing POST; therefore, when `media_end` returns, all preceding media frames have been delivered. If a forward POST fails, both queued and subsequent sends return that error. A successful response is a 3-integer ACK array; the three slots only validate integer shape, and the second slot does not bind to local RID, AID, or ofs. Upon HTTP 200 and a valid ACK, `RID+1` and `ofs+N` are committed; on failure, cancellation, or an invalid ACK, original values are retained.

The backchannel uses `RID=rpc`, the current SID, gsessionid, and AID. Each wire frame consists of a decimal length, LF, and the corresponding JSON text, where the length is counted in UTF-16 code units:

```text
<DECIMAL_UTF16_LENGTH><LF>
<JSON_TEXT>
```

The JSON root value contains repeated `[serverAID,payload]` envelopes. Once the payload is successfully decoded and published in order, the AID is committed as `max(currentAID,serverAID)`. Transient read failures after the initial backchannel is established retain the SID, gsessionid, and committed AID, and trigger a reconnect; initial establishment failures, terminal protocol states, explicit closes, or unrecoverable errors enter the shutdown pipeline. Session states follow `new -> handshaking -> ready -> reconnecting -> ready`, while the teardown path transitions through `closing -> closed`.

`tool_call` events convey single function calls; messages with multiple calls emit sequential events. `tool_call_cancellation.tool_call_ids` carries canceled IDs. New `session_resumption.session_token` values atomically replace prior tokens; subsequent setup frames carrying the token bind to the original account for resumption.

Live plain text and realtime music use model scope; Live audio/image and Robotics use `bidi-media:<modelID>` scope, prefixed with `build:` when routed through Build. Upstream Code 7 releases scope cooldown via `bidi_quota_reset_delay`. Client disconnects release account leases immediately.

The upstream Bidi client wire format uses 6-slot or 7-slot sparse arrays. Setup is located at outer field 7:

| Setup JSON Index | Protobuf Field | Content |
| ---: | ---: | --- |
| 0 | 1 | `models/<MODEL_ID>` |
| 1 | 2 | generation configuration |
| 2 | 3 | tools |
| 6 | 7 | `[sessionToken]`; empty array `[]` for Live dialogue and Robotics when no token is present; omitted for real-time translation and transcription |
| 7 | 8 | `[104857,[52428]]` buffering parameters; omitted for real-time translation and transcription |
| 9 | 10 | Input audio transcription parameters; empty array, with real-time transcription language written at sub-index `7` |
| 10 | 11 | Fixed empty array |
| 15 | 16 | Timezone `[null,null,null,null,[zone]]` |

The configuration for Live dialogue and Robotics uses an 18-slot array:

| JSON Index | Content |
| ---: | --- |
| 14 | Output modalities; TEXT=`[1]`, AUDIO=`[3]` |
| 15 | Live voice `[[["Zephyr"]]]` |
| 16 | thinking `[1,null,null,level]`; Live Minimal=`4`, Robotics High=`3` |
| 17 | MediaResolution fixed value `2` |

Real-time translation models do not accept MediaResolution and use a 31-slot configuration: index 13 is `0`, index 14 is `[3]`, and index 30 is TranslationConfig `[echoTargetLanguage 0/1, targetLanguageCode]`. Real-time transcription models accept TEXT output only, with a configuration of `[null×14,[1]]`. Neither includes voice or thinking. The upstream returns input and translated text at serverContent indices 5 and 6; for real-time transcription, index 10 returns the current accumulated interim input transcription (mapped to `interim_input_transcription`), and index 5 returns the final input transcription after audio ends.

Minimal outer setup shape:

```json
[
  null, null, null, null, null, null,
  [
    "models/<MODEL_ID>",
    [null, null, null, null, null, null, null, null, null, null, null, null, null, null, [3], [[["Zephyr"]]], [1, null, null, 4], 2],
    null,
    null, null, null,
    [],
    [104857, [52428]],
    null,
    [],
    [],
    null, null, null, null,
    [null, null, null, null, ["Asia/Taipei"]]
  ]
]
```

Subsequent client wire frames:

| Public Frame | Outer Position | Sub-message |
| --- | --- | --- |
| `text` | index 2 / field 3 | Realtime input index 4 stores text |
| `audio` | index 2 / field 3 | Realtime input index 1 stores `[mime,base64]` |
| `image` | index 2 / field 3 | Realtime input index 3 stores `[mime,base64]` |
| `media_end` | index 2 / field 3 | Realtime input index 2 is `1` |
| `tool_response` | index 3 / field 4 | Sub-message index 1 stores repeated `[name,Struct,id]` |

Setup and each subsequent client wire message write a fresh WAA proof into outer index `5` / field `6` prior to transmission. Snapshot binding inputs:

| Wire | Binding Prompt |
| --- | --- |
| setup | `models/<MODEL_ID>`, followed by each `name + " " + description` appended in declaration order, separated by a single space |
| text, audio, image, media end | Empty string |
| tool response | Call ID of the first function response |

Server business message indices:

| JSON Index | Content |
| ---: | --- |
| 1 | setup complete |
| 2 | server content |
| 3 | tool calls |
| 4 | tool cancellation |
| 5 | usage raw |
| 6 | go away raw |
| 7 | session resumption |

server content indices `0/1/2/4/5/6` correspond to model content, turn complete, interrupted, generation complete, input transcription, and output transcription, respectively; index `0` of model content is repeated Part. Transcription sub-indices `0/1/2/3` represent text, finished, duration milliseconds, and language code, respectively. Tool calls are located at sub-index `1` of message index `3`, where each item is `[name,Struct,id]`; tool cancellation is located at sub-index `0` of message index `4`, whose value is repeated call IDs. Session resumption sub-indices `0/1` are token and resumable, respectively. The object state error schema is `{"__sm__":{"status":[[[code,message]]]}}`; string payloads `"noop"`, `"close"`, and `"stop"` represent no-op, normal close, and error stop, respectively.

## 7. Public Endpoints, State Mappings, and Implementation

| Protocol | Endpoints |
| --- | --- |
| OpenAI Chat | `GET /v1/models`, `GET /v1/models/{id}`, `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses`, `POST /v1/responses/input_tokens`, `GET /v1/responses/{id}`, `DELETE /v1/responses/{id}`, `GET /v1/responses/{id}/input_items`, `POST /v1/responses/{id}/cancel` |
| OpenAI Embeddings | `POST /v1/embeddings` |
| OpenAI Images | `POST /v1/images/generations`, `POST /v1/images/edits` |
| OpenAI Speech | `POST /v1/audio/speech` |
| OpenAI Videos | `POST /v1/videos`, `GET /v1/videos`, `GET /v1/videos/{id}`, `DELETE /v1/videos/{id}`, `GET /v1/videos/{id}/content` |
| Anthropic Messages | `POST /v1/messages`, `POST /v1/messages/count_tokens` |
| Anthropic Models | `GET /v1/models`, `GET /v1/models/{id}` |
| Gemini Content | `POST /v1beta/models/{model}:generateContent`, `:streamGenerateContent`, `:countTokens` |
| Gemini Embeddings | `POST /v1beta/models/{model}:embedContent`, `:batchEmbedContents` |
| Gemini Videos | `POST /v1beta/models/{model}:predictLongRunning`, `GET /v1beta/operations/{id}` |

Extended endpoints:

| Protocol | Endpoints |
| --- | --- |
| OpenAI and Anthropic Files | `POST /v1/files`, `GET /v1/files`, `GET /v1/files/{id}`, `GET /v1/files/{id}/content`, `DELETE /v1/files/{id}` |
| OpenAI Transcription & Translation | `POST /v1/audio/transcriptions`, `POST /v1/audio/translations` |
| Realtime WebSocket | `GET /v1/live`, `GET /v1/robotics/stream` |
| Gemini Interactions | `POST /v1/interactions`, `POST /v1beta/interactions` |
| Gemini Files & Uploads | `POST /upload/v1beta/files`, `GET /v1beta/files`, `GET /v1beta/files/{id}`, `DELETE /v1beta/files/{id}` |
| Gemini Models | `GET /v1beta/models`, `GET /v1beta/models/{model}` |

When `ADMIN_AUTH_ENABLED=false`, the `/api` control plane requires the source address to be loopback and the `Host` to be localhost or a loopback address. Once authentication is enabled, access is granted via cookie-backed sessions issued upon providing the admin username and password. Administrative requests enforce same-origin validation and include `Cache-Control: no-store`. All responses include `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, and `Referrer-Policy: no-referrer`. `GET /health` returns `{"status":"ok"}`.

`POST /api/auth/login` accepts `{"username":"<ADMIN_USERNAME>","password":"<ADMIN_PASSWORD>"}` and returns `{"enabled":true,"authenticated":true,"username":"<ADMIN_USERNAME>"}` upon success. The session cookie is `aistudio_admin`, with Path `/api`, `HttpOnly`, and `SameSite=Strict`; `Secure` is appended under HTTPS or when the proxy provides `X-Forwarded-Proto: https`, with a 12-hour expiration. `GET /api/auth/session` returns the same status structure, with an empty username when unauthenticated. `POST /api/auth/logout` returns 204, invalidating the current session and aborting associated administrative requests. Failed logins return 401; after 5 consecutive failures from the same source within 1 minute, the endpoint returns 429 with `Retry-After: 60`.

| Control Capability | Endpoints |
| --- | --- |
| Admin Auth | `GET /api/auth/session`, `POST /api/auth/login`, `POST /api/auth/logout` |
| Status & Models | `GET /api/status`, `GET /api/models` |
| Service Controls | `POST /api/control/start`, `POST /api/control/stop` |
| Accounts | `GET /api/accounts`, `POST /api/accounts`, `GET/POST /api/accounts/import/chrome`, `PUT /api/accounts/{id}`, `DELETE /api/accounts/{id}`, `POST /api/accounts/{id}/login`, `POST /api/accounts/{id}/verify` |
| Remote Pairing | `POST /api/pairing`, `POST /api/pairing/accounts` |
| Requests & Usage | `GET /api/requests`, `GET /api/requests/{id}`, `POST /api/requests/{id}/cancel`, `GET /api/requests/{id}/body`, `GET /api/usage`, `GET /api/usage/records`, `GET /api/usage/records.csv` |
| Config & Logs | `GET /api/config`, `GET /api/logs/stream`, `DELETE /api/logs` |

The admin API uses DTOs (Data Transfer Objects) to represent request, response, and event payloads. Endpoint responses:

| Endpoint | Success Status | Body |
| --- | ---: | --- |
| `GET /api/status` | 200 | `AdminStatus` |
| `GET /api/models` | 200 | `{"models":[Model,...]}` |
| `GET /api/accounts` | 200 | `{"accounts":[AdminAccount,...]}` |
| `POST /api/accounts` | 201 | `{"account":AdminAccount}` |
| `GET /api/accounts/import/chrome` | 200 | `{"profiles":[ChromeImportProfile,...]}` |
| `POST /api/accounts/import/chrome` | 201 | `{"accounts":[AdminAccount,...]}` |
| `POST /api/pairing` | 201 | `PairingToken` |
| `POST /api/pairing/accounts` | 201 | `{"account":AdminAccount}` |
| `PUT /api/accounts/{id}` | 200 | `{"account":AdminAccount}` |
| `POST /api/accounts/{id}/login`, `verify` | 200 | `{"account":AdminAccount}` |
| `DELETE /api/accounts/{id}` | 204 | Empty body |
| `POST /api/control/start`, `stop` | 200 | `AdminStatus` |
| `GET /api/config` | 200 | `RuntimeConfig` |
| `GET /api/cooldowns` | 200 | `{"cooldowns":[AdminCooldown,...]}` |
| `GET /api/requests` | 200 | `{"requests":[AdminRequest,...]}` |
| `POST /api/requests/{id}/cancel` | 204 | Empty body |
| `GET /api/usage` | 200 | `UsageReport` |
| `GET /api/usage/records` | 200 | `UsageRecordPage` |
| `GET /api/usage/records.csv` | 200 | CSV attachment with UTF-8 BOM |
| `GET /api/requests/{id}/body` | 200 | `RequestBody` |
| `DELETE /api/logs` | 204 | Empty body |

| DTO | Fields |
| --- | --- |
| `AdminStatus` | `state`, `running`, `ready`, `version`, `active_requests`, `accounts` |
| `AdminAccountCounts` | `total`, `ready`, `busy`, `cooldown`, `auth_required` |
| `AdminAccount` | `id`, `label`, `enabled`, `state`, `proxy`, `locale`, `timezone`, `models`, `benefit_tier`, `message` |
| `AccountCreateInput` | `label`, `storage_state`, `proxy`, `locale`, `timezone` |
| `AccountUpdateInput` | `label`, `proxy`, `locale`, `timezone`, `enabled` |
| `ChromeImportInput` | `account_ids`, `proxy`, `locale`, `timezone` |
| `AccountStateInput` | `label`, `account`, `storage_state`, `runtime_state`, `fingerprint` |
| `PairingCreated` | `token`, `origin`, `command`, `expires_at` |
| `AdminModel` | `id`, `display_name`, `channels`, `input_token_limit`, `output_token_limit`, `methods`, `capabilities`, `capability_options`, `thinking_level`, `thinking_budget` |
| `AdminRequest` | `id`, `path`, `method`, `model`, `account`, `channel`, `status`, `duration_ms`, `input_tokens`, `output_tokens`, `total_tokens`, `created_at` |
| `RequestBody` | `request_id`, `path`, `method`, `model`, `time`, `request_content_type`, `response_content_type`, `request_body`, `response_body` |

`AdminStatus.state` can be `STOPPED`, `LAUNCHING`, or `RUNNING`; `running` is true only when in `RUNNING` state; `ready` requires `RUNNING` and at least one account in ready or busy state; `version` is sourced from build metadata; `active_requests` represents the number of active requests in the current process registry. `AdminAccount.message` records the reason for the current state, and `models` lists the real-time catalog IDs available to that account. `until` and `started_at` use RFC 3339 JSON timestamps.

`GET /api/usage` parameters:

| Parameter | Meaning |
| --- | --- |
| `from`, `to` | Required RFC 3339 timestamps |
| `tz` | IANA timezone, default UTC |
| `bucket` | Bucket duration in seconds `60`, `300`, `900`, `3600`, `86400`, or `auto` |
| `model`, `account`, `channel`, `protocol`, `state` | Dimensional filters, comma-separated values |
| `stack` | Stacking dimension for series, default `model` |

- The query window is `[from, to)`, max 90 days. `auto` selects buckets based on span duration.
- `UsageReport` contains summary metrics, timeline time-series, rankings across dimensions, and model performance latency percentiles.

The commit sequence for `PUT /api/accounts/{id}` is strictly ordered: validate the immutable email ID, acquire an exclusive lease on the account, create a new unpromoted egress, shut down the current worker and mark the new worker configuration as `pending` (unpromoted), atomically write `account.json` and update the account pool within a model catalog write lock, and subsequently promote the worker configuration, swap the egress, release the lease, and rebuild the model cache. Writing `account.json` is the sole point of durable commit. Any errors during egress creation, worker shutdown, or file write prior to commit discard the pending configuration and preserve the old configuration; any already-terminated worker is reconstructed according to the old configuration on subsequent requests. Once durably written, the new account configuration, worker configuration, and egress collectively transition to the committed state. A failure during lease release maintains this committed state while returning the original unlock error; a successful release logs completion and synchronizes the model cache.

`RuntimeConfig` fields. `response-only` indicates that the field is populated by the server and client-submitted values are not persisted:

| Field | Read/Write & Effective Timing |
| --- | --- |
| `auth_states`, `proxy`, `init_timeout`, `request_timeout` | Saved values; used upon next generation service startup |
| `warm_worker_limit`, `max_active_workers`, `warm_startup_concurrency`, `per_account_concurrency` | Saved values; used upon next generation service startup |
| `temporary_chat` | Saved value; used upon next generation service startup |
| `build_native_nonstream` | Saved value; determines if non-streaming requests prioritize Build |
| `upstream_channels` | Saved value; upstream channels for generation requests |
| `waa_backend` | Saved value; WAA lifecycle backend |
| `auto_start` | Auto-start generation service on application launch |
| `request_body_log` | Store request and response bodies up to 64 KiB each, keeping latest 1000 |
| `admin_auth_enabled`, `admin_username`, `admin_password_set` | Management auth settings; changes require application restart |

`GET /api/config` returns the current runtime configuration (read-only). Configuration is loaded at startup from environment variables or `.env`, and runtime modification via the API is not supported.

The initial event replay order for `GET /api/events` is `status`, `models`, `accounts`, the latest 200 `log` entries, `cooldowns`, and active `request` items sorted by start time. Subsequent event `data` payloads conform to:

| `type` | `data` |
| --- | --- |
| `status` | `AdminStatus` |
| `models` | `{"models":[Model,...]}` |
| `accounts` | `{"accounts":[AdminAccount,...]}` |
| `log` | `AdminLog` |
| `cooldowns` | `[AdminCooldown,...]` |
| `request` | `AdminRequest`, re-emitted with the same ID upon state transitions |

Administrative errors are standardized as:

```json
{"error":{"code":"invalid_request","message":"..."}}
```

Control plane error codes include `control_plane_forbidden`, `control_plane_origin_forbidden`, `invalid_request`, `invalid_account`, `account_not_found`, `account_busy`, `account_required`, `request_not_found`, and `upstream_error`.

| HTTP | Control Error Code |
| ---: | --- |
| 400 | `invalid_request`, `invalid_account`, `account_required` |
| 403 | `control_plane_forbidden`, `control_plane_origin_forbidden` |
| 404 | `account_not_found`, `request_not_found` |
| 409 | `account_busy` |
| Upstream status or 502 | `upstream_error` |

The runtime state machine operates as follows. Catalog fan-out denotes concurrent `ListModels` calls to all eligible accounts; pending accounts are those awaiting the next round of catalog retries:

```text
process start
  -> control plane ready
  -> STOPPED

POST /api/control/start
  -> LAUNCHING
  -> load CachedModels
  -> fan out ListModels to every enabled ready/busy account
  -> if cache is empty, wait for the first non-empty live catalog
  -> prewarm up to WARM_WORKER_LIMIT workers
     with WARM_STARTUP_CONCURRENCY bootstraps
  -> first worker ready
  -> RUNNING
  -> continue full catalog fan-out and remaining worker prewarm in background
  -> every 30s, fan out ListModels to every pending account

request
  -> resolve model and endpoint capability
  -> acquire one PER_ACCOUNT_CONCURRENCY slot
  -> prepare WAA proof
  -> send MakerSuite RPC
  -> stream frames
  -> release slot

POST /api/control/stop
  -> cancel launch or active requests
  -> bound catalog fan-out shutdown to 2s
  -> close WAA workers
  -> bound unfinished lifecycle-transition wait to 12s
  -> STOPPED
```

Request status uses `queued`, `running`, `completed`, `cancelled`, and `failed`. Video operations use `queued`, `completed`, and `failed`, corresponding to progress `0` or `100`.

The model catalog resides in memory for the duration of the current generation (`runtimeGeneration`, representing a single generation service instance); a new generation created by a clean Stop/Start sequence starts with an empty cache. At startup, the service establishes a public snapshot using `catalog.CachedModels()` from the current generation, immediately spawning a background `ListModels` task for each `enabled` account in `ready` or `busy` state. If the current generation's cache is non-empty, worker prewarming begins immediately. As each non-empty result arrives, it is merged into the public catalog from `CachedModels`, and updated `accounts` and `models` events are published; if the service is already `RUNNING`, this also triggers worker prewarming. The first non-empty result in a new generation unblocks the startup wait phase. When the full-account fan-out completes, an updated account and model snapshot is re-emitted if the `auth_required` set has changed, logging the number of successful syncs, non-empty responses, public models, pending retry accounts, and elapsed time. Once the first worker is ready, the current cache is re-evaluated, and the service transitions to `RUNNING` while holding the account and configuration mutation lock.

Account states:

| State | Scheduling Semantics |
| --- | --- |
| `ready` | Valid authentication with available concurrency slots |
| `busy` | Active exclusive operation, authentication refresh, or full request slots |
| `cooldown` | Global `*` cooldown active |
| `auth_required` | Account-level authentication failure |
| `unavailable` | Runtime currently unavailable |
| `disabled` | Configured as disabled |

- `busy` accounts evaluate available slots per `PER_ACCOUNT_CONCURRENCY`
- Model scope cooldowns do not alter account status, only affecting candidate routing

State persistence carries `authGeneration` and `checkedAt`, applying account objects, storage states, and fingerprints to disk atomically.

`ModelAccessKey(scope, model)` first strips the `models/` prefix; an empty scope returns the canonical model ID, whereas a non-empty scope returns `<scope>:<canonicalModelID>`. Capabilities map to the following keys:

| Capability | Scope | Success Recording |
| --- | --- | --- |
| Standard generation | `<modelID>` | Writes `verified` when canonical `EventFinish` arrives |
| CountTokens | `count-tokens:<modelID>` | Clears scope cooldown, preserves standard generation eligibility |
| Transcribe | `<modelID>` | Writes `verified` when text or segments are non-empty |
| Live text | `<modelID>` | Writes `verified` on setup complete; begins model check on each text frame |
| Live audio/image | `bidi-media:<modelID>` | Same as above |
| Robotics | `bidi-media:<modelID>` | Same as above |
| Veo creation | `veo` | Clears scope cooldown on operation ID |
| Drive upload | `drive` | Clears scope cooldown on successful Drive upload |

Code 7 retains the verified status of the current operation scope; authentication failures update the account-level state to `auth_required`. Non-generation failures, such as uploads or temp file cleanup errors, trigger a global `*` cooldown.

Text, reasoning, tools, usage, and first event metrics are recorded for diagnostics; mid-stream disconnects, client cancellations, or errors preserve existing verification states.

A successful Bidi setup records the `checkedAt` timestamp of the lease (the account lease held for the session). Each subsequent turn that updates model qualification is assigned a strictly increasing attempt timestamp within the session; `turn_complete` consumes the corresponding attempt and records qualification. Code 7 does not update model qualification. Bidi auth successes and 401 errors follow the same attempt sequence; a late 401 returned after setup can still transition the account to `auth_required`.

Once browser process termination is confirmed, process and worker states are set to `closed`. If shutdown fails, the worker, runtime lease, warm flag, and generation (worker instance version) are retained; subsequent Stop calls will re-execute the worker reset even if the service state is already `STOPPED`. Camoufox teardown phase upper bounds are 3 seconds for BiDi shutdown, 5 seconds for process termination and wait, and 2 seconds for profile deletion; errors across phases are joined and preserved using `errors.Join`.

The model catalog fan-out is bound to the context of the current generation service. Startup failures, startup cancellations, or Stop operations cancel all pending catalog tasks and wait up to 2 seconds to confirm background catalog goroutine exit. When Stop encounters an incomplete `LAUNCHING` state or another active start/stop lifecycle transition, it waits up to 12 seconds on the transition channel (notification channel for transition completion); this upper bound accommodates the 2-second catalog shutdown and bounded worker cleanup. Timeout errors and cleanup errors are returned aggregated via `errors.Join`.

On-demand hot replacement spawns a pending worker (a bootstrapping, unpromoted replacement worker) prior to shutting down the stale worker; the replacement becomes the active worker only after the old instance terminates cleanly. If shutting down the old instance and reclaiming the replacement worker fail simultaneously, both are retained for future cleanup, each occupying an active capacity slot; new worker creation halts once the capacity ceiling is reached. The sequence for a full generation service Stop/Start requires retrying the shutdown of the old instance before Start creates a new generation service; if the old PID fails to terminate, the stop error is returned and the original instance is preserved.

Worker process crashes, worker replacement, and protocol Code 5 rebuild the account worker and retry once on the same account.

The pending retry set for the model catalog maintains account IDs awaiting re-synchronization. Accounts are added during the initial full-account fan-out at startup or during single-account syncs following creation, login, or verification if any error occurs or if an empty catalog is returned; they are removed once a non-empty catalog is retrieved or when the account is deleted. After background sync for all accounts completes, a single 30-second ticker is started. Each tick fans out concurrently to the sorted list of pending accounts, re-verifying at task initiation that the ID remains in the pending set. Accounts producing errors or empty catalogs remain in the set; each non-empty success immediately updates the account cache and public catalog snapshot, emits `accounts` and `models` events, and triggers worker prewarming if in `RUNNING` state. At batch completion, an updated account and model snapshot is emitted if the `auth_required` set changed; on-demand single-account synchronizations immediately emit snapshots regardless of success or failure.

Model catalog projections:

| Rule | Result |
| --- | --- |
| OpenAI | Model list and equivalent single model object |
| Anthropic | Anthropic Models API format when `Anthropic-Version` is provided |
| Gemini | Model name formatted as `models/<ID>` |
| Single model lookup | Official canonical ID takes precedence over aliases |
| Multi-account consolidation | Methods, capabilities, options, and access modes are merged as a union; `paid` is logical OR |
| Multi-account token limits | Input and output token limits take the minimum positive values |
| Model aliases | Derived from ListModels field 57, exposed as independent models |

Complete admin model schema:

```json
{
  "id": "gemini-example",
  "name": "Gemini Example",
  "description": "...",
  "methods": ["countTokens", "generateContent"],
  "input_token_limit": 1048576,
  "output_token_limit": 65536,
  "capabilities": {"thinking": true, "capability_code_25": true},
  "capability_options": {"aliases": ["gemini-example-latest"]},
  "access_modes": [3, 4],
  "paid": true
}
```

OpenAI `GET /v1/models`:

```json
{
  "object": "list",
  "data": [{
    "id": "gemini-example",
    "object": "model",
    "created": 0,
    "owned_by": "google",
    "name": "Gemini Example",
    "description": "...",
    "supported_generation_methods": ["countTokens", "generateContent"],
    "input_token_limit": 1048576,
    "output_token_limit": 65536,
    "capabilities": {},
    "capability_options": {},
    "access_modes": [],
    "paid": true,
    "channels": ["playground", "build"]
  }]
}
```

When requested with an `Anthropic-Version` header, the same route returns:

```json
{
  "data": [{"type":"model","id":"gemini-example","display_name":"Gemini Example","created_at":"1970-01-01T00:00:00Z","max_input_tokens":1048576,"max_tokens":65536,"capabilities":null,"lifecycle":"active","line":null,"deprecated_at":null,"retires_at":null}],
  "has_more": false,
  "first_id": "gemini-example",
  "last_id": "gemini-example"
}
```

`max_input_tokens` and `max_tokens` take live catalog limits (null when missing). `limit` is an integer 1-1000 (default all). `after_id` and `before_id` paginate with `has_more`, `first_id`, and `last_id`. `lifecycle` returns empty when active is excluded. Single model lookup matches list item formatting.

In the admin model object, `description`, token limits, capabilities, capability options, access modes, and false `paid` flags use `omitempty`. OpenAI and Gemini responses always include model identity, methods, and token limits, appending extended fields when maps/slices are non-empty or when `paid=true`.

The management catalog merges live models across all accounts, sorted by ID. The public catalog contains models accessible by at least one enabled account with public protocol support; enabling Build includes Build-exclusive generatable models with `channels` indicating availability, see [Build Channel](build.md). Transient cooldowns are handled during request scheduling.

Primary request formats:

| Endpoint | Required Fields | Primary Result |
| --- | --- | --- |
| `/v1/chat/completions` | `model` | Chat completion or streaming delta chunks |
| `/v1/responses` | `model` | Response object or `response.*` events |
| `/v1/responses/input_tokens` | `model` or `previous_response_id` | `{"object":"response.input_tokens","input_tokens":<INT>}` |
| `/v1/files` | multipart `file`, optional `purpose` | File object |
| `/v1/files/{id}` | - | File object or deleted acknowledgment |
| `/v1/files/{id}/content` | - | Raw binary content stream |
| `/v1/audio/transcriptions` | multipart `file`, `model` | Transcription text or structured object |
| `/v1/audio/translations` | multipart `file`, `model` | Translation text or structured object |
| `/v1/audio/speech` | `model`, `input`, `voice` | Binary audio stream |
| `/v1/images/generations` | `model`, `prompt`, `n`, `size` | Image generation data object |
| `/v1/images/edits` | `model`, `prompt`, image/mask | Image generation data object |
| `/v1/videos` | `model`, `prompt`, `seconds`, `size` | Video operation object |
| `/v1/videos/{id}` | - | Video operation object or deleted acknowledgment |
| `/v1/videos/{id}/content` | - | Raw MP4 video binary stream |
| `/v1beta/models/{model}:predictLongRunning` | `instances` | Long-running operation object |
| `/v1beta/operations/{id}` | - | Long-running operation object |

The four generation entry points share a canonical request format, with input parameters mapped as follows:

| Capability | OpenAI Chat | OpenAI Responses | Anthropic | Gemini |
| --- | --- | --- | --- | --- |
| system | `system` / `developer` messages | `instructions` and system/developer message items | `system` string or text blocks | `systemInstruction` text parts |
| text | string or text content part | string, message item | string or text block | Part `text` |
| image/document | Base64 data URL, `file_id` | `input_image`, `input_file` | `image`, `document` source blocks | `inlineData`, `fileData` |
| audio | `input_audio` | `input_audio` item | `audio` source blocks | `inlineData`, `fileData` |
| video | `video_url`, YouTube URL, `file_id` | `input_video` item | `video` source blocks | `fileData` |
| tool calls | `tool_calls` array | `function_call`, `custom_tool_call` items | `tool_use`, `server_tool_use` blocks | `functionCall` parts |
| tool results | `tool` role message | `function_call_output`, `custom_tool_call_output` items | `tool_result`, `web_search_tool_result` blocks | `functionResponse` parts |
| YouTube | `video_url` / `input_video` | `input_video` | URL source | `fileData.fileUri` |
| function call | Assistant `tool_calls` | `function_call` item | `tool_use` block | `functionCall` Part |
| function result | Tool message | `function_call_output` item | `tool_result` block | `functionResponse` Part |
| structured output | `response_format` | `text.format` | — | `responseMimeType` and response schema |
| thinking | `reasoning_effort` or `reasoning.effort` | `reasoning.effort` | `thinking.budget_tokens`, `output_config.effort` | `thinkingConfig` |

Gemini attachments and image inputs for `predictLongRunning` accept `inlineData` / `inline_data`, `fileData` / `file_data`, `mimeType` / `mime_type`, and `fileUri` / `file_uri`. If duplicate aliases are supplied simultaneously, outer containers prioritize camelCase objects, and inner fields prioritize non-empty camelCase values.

Media Base64 inputs accept standard and URL-safe alphabets, optional `=` padding, and the `data:<MIME>;base64,` prefix. Inline GIF images and OpenAI video `input_reference` form attachments have their first frame extracted and re-encoded to PNG using the logical canvas dimensions and frame offsets before transmission. Transparent first frames preserve transparency; uncovered areas of opaque first frames are filled using the background color from the global color table.

OpenAI Chat and Anthropic drop historical messages that contain no parts after transformation; purely whitespace text, tool calls, tool results, and media retain their original content.

Generation parameter mapping:

| Parameter | Rule |
| --- | --- |
| OpenAI max tokens | `max_completion_tokens` takes precedence over `max_tokens` |
| Anthropic max tokens | `max_tokens` maps to generation config field 4 |
| Gemini max tokens | `maxOutputTokens` maps to generation config field 4 |
| temperature / topP / topK / seed | Map to generation config fields 5 / 6 / 7 / 19 |
| stop sequences | Maps to generation config field 8 |
| safety settings | Gemini `safetySettings` maps to root field 3; image routes omit defaults |
| structured outputs | `response_format`, `output_format`, or `responseSchema` encoded as JSON Schema |

### OpenAI Chat Completions

`POST /v1/chat/completions` request fields:

| Field | Type and Semantics |
| --- | --- |
| `model` | Required model ID |
| `messages` | Array of message objects |
| `stream` | boolean |
| `stream_options.include_usage` | Emit usage-only chunk after finish chunk |
| `tools` | Array of functions, custom tools, or Google server tools |
| `tool_choice` | Omitted / `auto` / `none` / `required`, named function, or `allowed_tools` |
| `web_search_options` | Search configuration parameters |
| `response_format` | `text`, `json_object`, or `json_schema` |
| `reasoning_effort` | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |
| `temperature`, `top_p`, `seed` | Sampling parameters |
| `max_completion_tokens`, `max_tokens` | Output token limits |
| `stop` | string or string array |
| `media_resolution` | `low`, `medium`, `high` input resolution |

```json
{
  "id": "call_01",
  "type": "function",
  "function": {"name":"get_weather","arguments":"{\"city\":\"Taipei\"}"},
  "extra_content": {"google":{"thought_signature":"<SIGNATURE>"}}
}
```

`content` can be a string or an array of Parts. Part fields:

| `type` | Other Fields |
| --- | --- |
| `text`, `input_text`, `output_text` | `text` |
| `image_url`, `input_image` | `image_url` string or `{"url":"..."}`; or `file_id` |
| `video_url`, `input_video` | `video_url` string or `{"url":"..."}` |
| `input_file`, `file` | `file_id`, or `filename` + `file_data` |
| `input_audio` | `input_audio: {data, format}` |
| `refusal` | Text content part |

Non-streaming response:

```json
{
  "id": "chatcmpl_...",
  "object": "chat.completion",
  "created": 0,
  "model": "gemini-example",
  "provider_model": "gemini-provider-id",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "...",
      "reasoning_content": "...",
      "tool_calls": [],
      "annotations": []
    },
    "logprobs": null,
    "finish_reason": "stop",
    "provider_finish_reason": "provider_19"
  }],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30,
    "completion_tokens_details": {"reasoning_tokens": 5}
  }
}
```

`provider_model`, `provider_finish_reason`, `reasoning_content`, `tool_calls`, `annotations`, and `usage` appear only when corresponding data is present. The annotation structure is `{"type":"url_citation","url_citation":{"url","title","start_index","end_index"}}`.

Chat SSE sequence:

1. Role chunk for each choice: `delta={"role":"assistant","content":""}`
2. Content in `delta.content`, reasoning in `delta.reasoning_content`, tool calls in `delta.tool_calls`
3. Citations in `delta.annotations` at choice conclusion
4. Finish chunk with `finish_reason` and optional `provider_finish_reason`
5. Final chunk with `usage` when `include_usage=true`

### OpenAI Responses

`POST /v1/responses` request fields:

| Field | Type and Semantics |
| --- | --- |
| `model` | Required model ID |
| `input` | string or array of input items |
| `instructions` | Top-level system instructions |
| `stream` | boolean |
| `tools`, `tool_choice` | function, custom, namespace, local_shell, shell, apply_patch, and Google tools; choice supports auto/none/required, named tools, and managed types |
| `temperature`, `top_p`, `max_output_tokens` | Sampling parameters |
| `truncation` | Context truncation policy |
| `media_resolution` | Media resolution specification |
| `reasoning` | `{"effort":"..."}` |
| `text` | `{"format":{"type":"text|json_object|json_schema","schema":...}}` |
| `previous_response_id` | Previously saved response ID within current process |
| `parallel_tool_calls` | Omitted / `true` / `false` |
| `truncation` | Omitted / `disabled` / `auto` |
| `metadata` | string-to-string object |
| `store` | Omitted / `true` / `false` |
| `background` | boolean |

- Namespaced functions encode with qualified names, restored on output
- `store` saves session nodes in-process when true or omitted; false returns one-off results
- `background: true` executes within current connection and returns completed response, saved regardless of `store`

Input items have `type`, `id`, `role` fields:

| type | Fields |
| --- | --- |
| `input_text`, `output_text` | `text` |
| `input_image` | `image_url` string or `{url}`; or `file_id` |
| `input_file` | `file_id`, `file_url`, or `filename` + `file_data` |
| `input_audio` | `input_audio: {data, format}` |
| `input_video` | `video_url` string or `{url}` |
| `refusal` | Text message content |

Image, video, and file Parts in Responses reuse the data URL, file ID, and YouTube rules described above.

Additional input item mappings:

| item | Mapping |
| --- | --- |
| system/developer message | Text enters system instructions, images and files move to first user turn |
| `custom_tool_call` | Function call with arguments `{"input": <INPUT>}` |
| `local_shell_call` | `local_shell` call with action omitting `type` |
| `shell_call` | `shell` call with action argument |
| `apply_patch_call` | `apply_patch` call with patch argument |
| `function_call_output` | Tool result with matching call ID |
| `custom_tool_call_output` | Output string or object |

| tool type | Fields |
| --- | --- |
| `function` | `name`, `description`, `parameters`, `strict` |
| `web_search*` | `search_context_size`, `user_location`, `filters.allowed_domains` |
| `image_search`, `url_context`, `google_maps` | `type` |
| `code_interpreter` | Container definition |
| `local_shell`, `shell`, `apply_patch` | System developer tools |

The response shell fields are always present:

```json
{
  "id": "resp_...",
  "object": "response",
  "created_at": 0,
  "completed_at": null,
  "status": "in_progress",
  "error": null,
  "incomplete_details": null,
  "instructions": null,
  "metadata": {},
  "model": "gemini-example",
  "output": [],
  "output_text": "",
  "parallel_tool_calls": true,
  "previous_response_id": null,
  "reasoning": null,
  "temperature": null,
  "text": {"format":{"type":"text"}},
  "tool_choice": "auto",
  "tools": [],
  "top_p": null,
  "truncation": "disabled",
  "max_output_tokens": null,
  "usage": null
}
```

Completed objects may additionally include `provider_model` and `provider_finish_reason`. `status` is `completed`, `incomplete`, or `failed`; length-based terminal states use `incomplete_details.reason=max_output_tokens`, and policy-based terminal states use `content_filter`.

Output item union types:

| type | Fields |
| --- | --- |
| `reasoning` | `id`, `status`, `summary: [{type: "summary_text", text}]`, optional `encrypted_content` |
| `message` | `id`, `status`, `role: "assistant"`, `content: [{type: "output_text", text, annotations}]` |
| `function_call` | `id`, `status`, `call_id`, `name`, `arguments` |
| `custom_tool_call` | `id`, `status`, `call_id`, `name`, `input` |
| `local_shell_call`, `shell_call`, `apply_patch_call` | Tool call output objects |

`web_search_call.action` is `{"type":"search","query":"...","sources":[{"type":"url","url":"..."}]}`, where queries are deduplicated by their first appearance and sources are deduplicated by URI. `code_interpreter_call.status` is `incomplete` when there are no results, `completed` on success, and `failed` for non-`OUTCOME_OK` outcomes; stdout is written to `outputs[].logs`, and failure text is prefixed with `stderr:`. Streaming `output_item.added` uses `in_progress`, and the corresponding `output_item.done` uses the final status.

Responses usage:

```json
{
  "input_tokens": 10,
  "output_tokens": 20,
  "total_tokens": 30,
  "input_tokens_details": {"cached_tokens":0},
  "output_tokens_details": {"reasoning_tokens":5}
}
```

Every Responses SSE payload contains `type` and a monotonically increasing `sequence_number` starting from 0:

| Event | Event Fields |
| --- | --- |
| `response.created`, `response.in_progress` | `response` shell |
| `response.output_item.added`, `response.output_item.done` | `output_index`, `item` |
| `response.reasoning_summary_part.added`, `done` | `item_id`, `output_index`, `summary_index`, `part` |
| `response.reasoning_summary_text.delta` | Text delta in reasoning summary |
| `response.output_text.delta` | Main output text delta |
| `response.function_call_arguments.delta` | Function arguments streaming delta |
| `response.completed` | Final completed response object |

### Anthropic Messages

`POST /v1/messages` request fields:

| Field | Type & Semantics |
| --- | --- |
| `model` | Required model ID |
| `messages` | `{role, content}` array (`user`, `assistant`, `system`) |
| `system` | string or text block array |
| `max_tokens` | Positive integer, defaults to model output limit |
| `stop_sequences` | string array |
| `stream` | boolean |
| `temperature`, `top_p`, `top_k` | Sampling parameters |
| `tools`, `tool_choice` | custom and managed tools |
| `output_config.effort` | Reasoning effort |
| `media_resolution` | Input media resolution |

| Block Type | Fields |
| --- | --- |
| `text` | `text` |
| `thinking` | `thinking`, `signature` |
| `redacted_thinking` | `data` |
| `image`, `document` | `source: {type, media_type, data, url, file_id, content}` |
| `tool_use` | `id`, `name`, object `input` |
| `tool_result` | `tool_use_id`, `content`, `is_error` |
| `server_tool_use` | `id`, `name`, `input` |
| `web_search_tool_result` | Web search citations and source blocks |

A custom tool is defined as `{name,description,input_schema}`, with an optional `type:"custom"`. Server tool fields:

| type | Required name |
| --- | --- |
| `web_search_*` | `web_search` |
| `image_search` | `image_search` |
| `web_fetch_*` | `web_fetch` |
| `code_execution_*` | `code_execution` |
| `url_context` | `url_context` |
| `google_maps` | `google_maps` |

`web_search_*` options map `allowed_domains` and `user_location` to search preferences. `bash_*`, `text_editor_*`, and `memory_*` declare functions using request names with standardized parameters.

Non-streaming response:

```json
{
  "id": "msg_...",
  "type": "message",
  "role": "assistant",
  "model": "gemini-example",
  "content": [
    {"type":"thinking","thinking":"...","signature":"..."},
    {"type":"text","text":"..."},
    {"type":"tool_use","id":"call_01","name":"get_weather","input":{}}
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "provider_model": "gemini-provider-id",
  "provider_finish_reason": "provider_19",
  "usage": {"input_tokens":10,"output_tokens":20}
}
```

Content output blocks can be `text`, `thinking`, `redacted_thinking`, `tool_use`, `server_tool_use`, or `web_search_tool_result`. Stop reasons include `end_turn`, `tool_use`, `stop_sequence`, `max_tokens`, `pause_turn`, or `refusal`. `POST /v1/messages/count_tokens` accepts the same message/system/tools input and returns `{"input_tokens":<INT>}`; it provides an independent count estimate, whereas final usage for media requests like PDFs is determined by `usage.input_tokens` in the generation response.

Media in Anthropic responses is encoded as Markdown data URLs within text blocks, and code is encoded as fenced text. Each deduplicated Google Search query generates a pair of `server_tool_use` and `web_search_tool_result`; results are deduplicated by URL, with the sources covering all queries from this search. `usage.server_tool_use.web_search_requests` records the upstream-reported deduplicated query count, and `web_fetch_requests` is `0`. Other citations, such as URL Context, append a Markdown `Sources:` list at the end.

Search sources generate `encrypted_content` containing upstream URLs, titles, and snippets. Client roundtrips reconstruct text continuation with queries formatted as `Web search: <query>`, results appended in the same text block, and snippets decrypted when matching `PROXY_API_KEY`. Failed searches format as `Web search failed: <error_code>`.

Anthropic SSE:

| Event | Payload |
| --- | --- |
| `message_start` | `{type,message:{id,type,role,model,content:[],stop_reason:null,stop_sequence:null,usage}}` |
| `content_block_start` | `{type,index,content_block}` |
| `content_block_delta` | `{type,index,delta}` |
| `content_block_stop` | `{type,index}` |
| `message_delta` | `{type,delta:{stop_reason,stop_sequence,provider_finish_reason?},usage}` |
| `message_stop` | `{type:"message_stop"}` |
| `error` | `{type:"error",error:{type,message}}` |

The delta union type includes `text_delta{text}`, `thinking_delta{thinking}`, `signature_delta{signature}`, and `input_json_delta{partial_json}`. The thinking signature is sent before the corresponding thinking block closes; redacted thinking uses a single start/stop block; `tool_use` first sends an empty input, followed by the complete argument JSON via `input_json_delta`. Search blocks are output as complete start/stop blocks once sources are aggregated, and query counts are returned with the final `message_delta.usage`.

### Gemini Interactions

`POST /v1beta/interactions` and `POST /v1/interactions` accept the same creation request, authenticated via `x-goog-api-key`, Bearer token, or the `key` query parameter.

```json
{
  "model": "gemini-3.8-flash-tts",
  "input": [{"type":"user_input","content":[{
    "type":"text","text":"Have a wonderful day!",
    "annotations":[{"type":"speech_metadata","style":"cheerful and friendly"}]
  }]}],
  "response_format": {"type":"audio","mime_type":"audio/l16","sample_rate":24000},
  "generation_config": {"speech_config":[{"voice":"Kore"}]},
  "stream": true
}
```

| Field | Mapping |
| --- | --- |
| `input` | string, content block, or step array (`text`, `image`, `audio`, `video`, `document`) |
| media content | `data` (Base64) or `uri`, optional `mime_type` |
| input steps | `user_input`, `model_output`, `thought`, `function_call`, `function_result`, `code_execution_call`, `code_execution_result` |
| `system_instruction` | System prompt string or block |
| `generation_config` | Sampling, thinking, and modal options |
| `tools`, `tool_choice` | Function declarations and tool choices |
| `response_format` | Response modal formats (type and MIME) |

Non-streaming responses contain `id`, `object:"interaction"`, `model`, `created`, `updated`, `status`, `steps`, and `usage`. `model_output.content` in `steps` stores text or media, with audio located in `{type:"audio",data,mime_type,sample_rate,channels}`; SDK `output_audio` and `output_text` are read from these steps. Function calls are returned as `function_call` steps with status `requires_action`; normal generation yields `completed`, while early termination like output limit exhaustion yields `incomplete`.

SSE uses matching event names and JSON `event_type`: `interaction.created` → `step.start` → `step.delta` → `step.stop` → `interaction.completed`. Steps correlate by `index`, and audio deltas are `{type:"audio",data,mime_type,sample_rate,channels}`. If no semantic events occur for 10 seconds, `: ping` is sent. Upstream errors or a missing terminal state trigger an `error` event and terminate the stream; client disconnections cancel upstream generation. Errors occurring prior to stream initiation use Gemini HTTP error objects.

`GET /v1beta/interactions/{id}` 返回创建时的完整 interaction；`include_input=true` 时 `steps` 先列出请求输入，字符串与连续的内容块合并为 `user_input` 步骤；`stream=true` 时按创建流的事件格式重放保存的 interaction，每个事件带递增的 `event_id`，查询参数 `last_event_id` 从其后的事件继续，无法识别时返回 `400 INVALID_ARGUMENT`。`POST /v1beta/interactions/{id}/cancel` 返回该 interaction 的终态对象。`DELETE /v1beta/interactions/{id}` 返回 `{}`，之后检索返回 404 `NOT_FOUND`，作为 `previous_interaction_id` 或 Responses 的 `previous_response_id` 返回 400。`/v1` 前缀行为相同。

### Embeddings

`POST /v1/embeddings` 请求字段为 `model`、`input`，可选 `dimensions`、`encoding_format`。`input` 是字符串或字符串数组，token 数组与空数组返回 400；`dimensions` 为正整数，写入每条请求的 `outputDimensionality`；`encoding_format` 为 `float`（默认）或 `base64`，`base64` 为小端 float32。响应为 `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[...]}],"model":"<MODEL>","usage":{"prompt_tokens":N,"total_tokens":N}}`。

`:embedContent` 接受 `content`、`taskType`、`title`、`outputDimensionality`，返回 `{"embedding":{"values":[...]}}`。`:batchEmbedContents` 接受 `requests[]`，每项字段相同并可带与路径一致的 `model`，返回 `{"embeddings":[...]}`。字段同时接受 snake_case；缺 `content` 或 `model` 与路径不一致时返回 400 `INVALID_ARGUMENT`。

三个端点都经 Build 通道调用上游 `batchEmbedContents`，超过 100 条时按顺序分批并合并结果，上游 `tokenCount` 计为输入 token。Build 未启用或没有可调用账户时返回 404 `model_not_found` / `NOT_FOUND`。

### Gemini GenerateContent

`POST /v1beta/models/{model}:generateContent`, `:streamGenerateContent`, and `:countTokens` accept:

```json
{
  "contents": [{"role":"user","parts":[{"text":"Hello"}]}],
  "systemInstruction": {"role":"user","parts":[{"text":"Be concise"}]},
  "generationConfig": {},
  "tools": [],
  "toolConfig": {},
  "safetySettings": [{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_ONLY_HIGH"}]
}
```

Request message fields accept both snake_case proto names and lowerCamelCase names (e.g. `system_instruction`, `generation_config.response_mime_type`, `tools[].function_declarations`). When both co-exist, camelCase takes precedence. Function arguments and schemas accept raw JSON objects.

| Part | Fields |
| --- | --- |
| text/thought | `text`, optional `thought`, `thoughtSignature` |
| inline data | `inlineData:{mimeType,data}` |
| file data | `fileData:{mimeType?,fileUri,displayName}` |
| function call | `functionCall:{id,name,args}` |
| function response | `functionResponse:{id,name,response}` |
| executable code | `executableCode:{language,code}` |
| code result | `codeExecutionResult:{outcome,output,error}` |

Parts lacking data fields and `thoughtSignature` are omitted; parts with multiple data fields return `400 INVALID_ARGUMENT`. `fileData.fileUri` supports YouTube URLs as external videos, `/upload/v1beta/files` or `/v1/files` as file references, and other http(s) URLs downloaded as inline media up to 512 MiB.

| Category | Fields |
| --- | --- |
| sampling | `temperature`, `topP`, `topK`, `frequencyPenalty`, `presencePenalty`, `seed` |
| media | `mediaResolution`: `MEDIA_RESOLUTION_LOW`, `MEDIUM`, `HIGH` |
| output limits | `candidateCount`, `maxOutputTokens`, `stopSequences` |
| log probabilities | `responseLogprobs`, `logprobs` |
| modalities | `responseModalities`: `["TEXT"]`, `["AUDIO"]`, `["IMAGE"]` |
| speech config | `speechConfig` and `multiSpeakerVoiceConfig` |
| thinking config | `thinkingConfig.thinkingLevel` or `thinkingBudget` |
| modalities | `responseModalities` |
| image | `imageConfig:{aspectRatio,imageSize}` |
| thinking | `thinkingConfig:{thinkingBudget,thinkingLevel}` |
| transcription | `transcriptionConfig:{languageCodes,customVocabulary,wordTimestamps,speakerLabels,smartTranscription}` |
| speech | `speechConfig` |

`candidateCount` accepts 0 to 8; values > 1 generate separate candidates concurrently, candidate 0 keeping the original request ID and candidate i using `<id>-<i>`. `:streamGenerateContent` begins output once the first candidate is ready, aggregating total usage across all candidates.

`responseMimeType` and `responseJsonSchema` map structured output constraints.

`speechConfig.voiceConfig` and `multiSpeakerVoiceConfig` are mutually exclusive; single-voice configurations must provide `prebuiltVoiceConfig.voiceName`, and each multi-speaker entry must provide a non-empty `speaker` and `voiceConfig.prebuiltVoiceConfig.voiceName`; `multiSpeakerVoiceConfig.mode` can be `VERBATIM` or `CONVERSATIONAL`. The `speechMetadata` (or `speech_metadata`) `{speaker,style}` of a text part is written to Part field 41. Models with capability code 85 split text without a speaker line by line, starting a new segment on lines prefixed with a configured speaker name and colon, merging continuation lines into the previous segment, and dropping any text prior to the first speaker line; text without matching lines is sent as-is. Legacy TTS models write `speechMetadata` back as a `speaker: line` prefix and a `style\n\n` instruction paragraph. `transcriptionConfig.smartTranscription=true` is mutually exclusive with explicitly set true for `wordTimestamps` or `speakerLabels`; the language code `detect` is normalized to empty for auto-detection.

Single-voice speech config:

```json
{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Kore"}}}
```

Multi-speaker:

```json
{
  "multiSpeakerVoiceConfig": {
    "speakerVoiceConfigs": [{
      "speaker": "Speaker A",
      "voiceConfig": {"prebuiltVoiceConfig":{"voiceName":"Kore"}}
    }]
  }
}
```

tool group fields:

| Tool | Fields |
| --- | --- |
| functions | `functionDeclarations:[{name,description,parameters,parametersJsonSchema}]` |
| search | `googleSearch` or `googleSearchRetrieval` |
| URL | `urlContext` |
| code | `codeExecution` |
| maps | `googleMaps` |
| image search | `imageSearch` |

`googleSearch.searchTypes` supports `webSearch` and `imageSearch` (defaulting to web search). `timeRangeFilter.startTime/endTime` uses RFC 3339 Nano. Tool choices support `AUTO`, `ANY`, `NONE`, or `VALIDATED` mode with allowed function names.

`:countTokens` returns:

```json
{"totalTokens": 123}
```

Non-streaming generation response:

```json
{
  "candidates": [{
    "content": {"role":"model","parts":[]},
    "index": 0,
    "finishReason": "STOP",
    "finishMessage": "...",
    "groundingMetadata": {},
    "citationMetadata": {}
  }],
  "modelVersion": "gemini-provider-id",
  "responseId": "request-id",
  "usageMetadata": {
    "promptTokenCount": 10,
    "candidatesTokenCount": 20,
    "thoughtsTokenCount": 5,
    "toolUsePromptTokenCount": 0,
    "totalTokenCount": 35
  }
}
```

`finishMessage` conveys upstream termination explanations (e.g. content filtering reasons). When `candidateCount` > 1, candidates are indexed 0 to n-1 with individual finish reasons. Streaming blocks emit candidates as they arrive, with final blocks listing all finish reasons and aggregated usageMetadata.

```json
{
  "text": "...",
  "transcriptionMetadata": {
    "speaker": "Speaker 1",
    "timestamps": [{
      "start":{"seconds":0,"nanos":0},
      "end":{"seconds":1,"nanos":250000000}
    }]
  }
}
```

`groundingMetadata` fields are `searchEntryPoint`, `groundingChunks`, `groundingSupports`, `retrievalMetadata`, `webSearchQueries`, and `googleMapsWidgetContextToken`. `searchEntryPoint` contains `renderedContent`, `sdkBlob`; `groundingChunks` element oneof is `web:{uri,title}`, `retrievedContext:{uri,title,text}`, or `maps:{uri,title,text,placeId}`; `groundingSupports` elements contain `segment:{partIndex,startIndex,endIndex,text}`, `groundingChunkIndices`, and optional `confidenceScores`; `retrievalMetadata` contains `googleSearchDynamicRetrievalScore`. Elements of `citationMetadata.citationSources` contain `uri`, `title`, `startIndex`, and `endIndex`.

Non-streaming results merge adjacent, homogeneous body or thought fragments that have no signature boundaries. Parts with tools, media, and transcription metadata remain separate; standalone signatures attach to the preceding unsigned Part, or fall back to `{"text":"","thought":true,"thoughtSignature":"..."}` when no attachable content exists.

`:streamGenerateContent` uses SSE. Each semantic event sends a partial `GenerateContentResponse` containing `responseId`, `modelVersion`, and a candidate Part, grounding, or citation; the final frame contains the candidate `finishReason`, optional `finishMessage`, and `usageMetadata`. Error frames after response headers are `data: {"error":{"code","message","status"}}`.

### Files, Transcribe, and Media

`POST /v1/files` accepts multipart `file` and optional `purpose` in any field order. Max file size is 512 MiB with 1 MiB multipart overhead. Content-Type is auto-detected from binary magic bytes when missing or generic octet-stream.

File object:

```json
{
  "id": "file_...",
  "object": "file",
  "bytes": 1234,
  "created_at": 0,
  "filename": "document.pdf",
  "purpose": "assistants",
  "status": "processed"
}
```

`POST /v1/files` and `GET /v1/files/{id}` return this object. `GET /v1/files/{id}/content` returns the raw body, setting `Content-Type`, attachment `Content-Disposition`, and `Content-Length` when known. `DELETE /v1/files/{id}` returns:

```json
{"id":"file_...","object":"file","deleted":true}
```

Unknown files return 404 `file_not_found`, oversized files return 413 `file_too_large`. Drive bindings and cross-account file cloning are detailed in "Drive Upload and File Part".

`GET /v1/files` returns a paginated list ordered newest-to-oldest with `has_more`, `first_id`, and `last_id`.

`POST /v1/audio/transcriptions` multipart fields:

| Field | Values and Handling |
| --- | --- |
| `file` | Required non-empty; `audio/*`, `video/mp4`, `video/webm`; max 512 MiB |
| `model` | Default `gemini-3.5-transcribe` |
| `response_format` | `json`, `text`, `srt`, `verbose_json`, `vtt`, `diarized_json` (default `json`) |
| `language` | Language code; `detect` and `auto` map to automatic detection |
| `prompt` | Preceding text context |
| `temperature` | 0-2 (default 0) |

```json
{
  "task": "transcribe",
  "language": "en",
  "duration": 1.25,
  "text": "Hello",
  "segments": [{"id":0,"start":0,"end":1.25,"text":"Hello","speaker":"Speaker 1"}],
  "words": [{"word":"Hello","start":0,"end":1.25,"speaker":"Speaker 1"}],
  "usage": {"input_tokens":10,"output_tokens":2,"total_tokens":12}
}
```

`verbose_json` and `diarized_json` use the same verbose object shape. `language` is the normalized request language; it is empty and omitted when `detect` / `auto`. `segments` originates from response Part field 23; `words` is generated when the word count matches the timestamp span count.

`POST /v1/audio/translations` transcribes input audio then translates transcription text to English: when `model` is a transcribe model or omitted, translation is performed by `gemini-flash-latest`; otherwise transcription runs on `gemini-3.5-transcribe` and translation on the specified model.

| Field | Values and Handling |
| --- | --- |
| `model` | Defaults to first generatable `image_route` model |
| `prompt` | Required text prompt |
| `n` | Number of images (default 1, range 1-10) |
| `size` | `auto` or `WIDTHxHEIGHT` |
| `quality` | `auto`, `low/standard=1K`, `medium/hd=2K`, `high/xhigh/max=4K` |
| `response_format` | `b64_json` returns Base64; other returns data URL |
| `stream` | Streaming support |

`POST /v1/audio/speech`:

| Field | Values and Handling |
| --- | --- |
| `model`, `input` | Required model ID and input text |
| `voice` | Voice name (default `Zephyr`) |
| `response_format` | Audio format (default `wav`; supports `mp3`, `opus`, `aac`, `flac`, `wav`, `pcm`) |
| `speed` | Speed multiplier `0.25..4.0` |
| `instructions` | Speech style instructions in `speechMetadata.style` |

Voice requests for legacy TTS models prepend `## Transcript:\n` before the first text in accordance with the official wire format; AUDIO-only generation configs omit the default `maxOutputTokens`; `responseModalities` and `speechConfig` are written to their officially confirmed slots respectively.

### Video

OpenAI `POST /v1/videos` accepts JSON or multipart:

| Field | Values and Handling |
| --- | --- |
| `model` | Defaults to first model supporting `predictLongRunning` |
| `prompt` | Required generation prompt |
| `seconds` | Video duration in seconds (default 4; snaps to nearest option) |
| `size` | Aspect ratio and resolution string (e.g. `1280x720`, `1920x1080`) |
| `input_reference` | Image file ID, data URL, or multipart upload for image-to-video |

```json
{
  "id": "operation-id",
  "object": "video",
  "model": "veo-example",
  "status": "queued",
  "progress": 0,
  "created_at": 0,
  "size": "1280x720",
  "seconds": "4"
}
```

`GET /v1/videos/{id}` returns video operation status (`queued`, `completed`, `failed`) and progress. `GET /v1/videos/{id}/content` downloads the completed video stream, returning 409 `video_not_ready` while in progress and 404 when deleted.

Upon successful video creation, the following resource binding is written keyed by operation ID:

```json
{
  "kind": "video-operation",
  "created_at": "2026-01-01T00:00:00Z",
  "video": {
    "model": "veo-example",
    "seconds": "4",
    "size": "1280x720"
  }
}
```

Model parameters, actual `seconds`, `size`, and UTC timestamps are persisted in resource bindings, enabling consistent polling across application restarts. Public timestamps use Unix seconds.

Gemini `:predictLongRunning` request:

```json
{
  "instances": [{
    "prompt": "...",
    "image": {
      "inlineData": {"mimeType":"image/jpeg","data":"..."}
    }
  }],
  "parameters": {
    "numberOfVideos": 1,
    "sampleCount": 1,
    "aspectRatio": "16:9",
    "durationSeconds": 4,
    "resolution": "720p"
  }
}
```

`instances` requires a single prompt; image inputs select between `inlineData` and `fileData`. `numberOfVideos` defaults to 1 (range 1-4). Duration, aspect ratio, and resolution snap to catalog capabilities.

`GET /v1beta/operations/{id}`:

```json
{
  "name": "operations/<ID>",
  "done": true,
  "response": {
    "generateVideoResponse": {
      "generatedSamples": [{
        "video": {"uri":"http://<HOST>/v1/videos/<ID>/content","mimeType":"video/mp4"}
      }]
    }
  }
}
```

`response` appears when all video tasks complete, listing `generatedSamples` with URIs pointing to `/v1/videos/<ID>/content`.

### Live and Robotics Public Frames

Send setup within 10 seconds after connection upgrade:

```json
{
  "type": "setup",
  "model": "gemini-live-model",
  "input_modalities": ["text", "audio", "image"],
  "output_modalities": ["audio"],
  "tools": [{"name":"get_weather","description":"...","parameters":{"type":"object"}}],
  "session_token": ""
}
```

Live input modalities are non-empty subsets of text/audio/image. Standard Live models output `["audio"]`; realtime translation models (capability 46) output audio with translated transcripts; realtime transcription models (capability 77) output text.

| type | Fields |
| --- | --- |
| `text` | `text` |
| `audio` | `mime_type: "audio/pcm"`, `data` |
| `image` | `mime_type: "image/jpeg"`, `data` |
| `media_end` | No extra fields |
| `tool_response` | `tool_responses: [{id, name, content}]` |
| `music_prompts` | `weighted_prompts: [{text, weight}]` |
| `music_config` | Realtime music configuration object |

```json
{
  "type": "text",
  "model": "gemini-live-model",
  "text": "...",
  "mime_type": "audio/l16;rate=24000",
  "data": "<BASE64>",
  "transcription": {"text":"...","finished":true,"duration_ms":1000,"language_code":"en"},
  "tool_call": {"id":"call-1","name":"get_weather","arguments":{}},
  "tool_call_ids": ["call-1"],
  "session_token": "...",
  "resumable": true,
  "raw": {},
  "error": "...",
  "code": "...",
  "retryable": true
}
```

Corresponding fields are used according to `type`: `session_opened{model}`, `setup_complete`, `text{text}`, `media{mime_type,data}`, `input_transcription/output_transcription/interim_input_transcription{transcription}`, `tool_call{tool_call}`, `tool_call_cancellation{tool_call_ids}`, `interrupted`, `generation_complete`, `turn_complete`, `session_resumption{session_token,resumable}`, `usage{raw}`, `go_away{raw}`, `provider{raw}`, `closed`, and `error{error,code,retryable,raw?}`.

If the initial frame or subsequent client fields are invalid, `{type:"error",code:"invalid_request",error:"..."}` is sent. Upstream errors retain their original `error` content. The client single-frame size limit is 8 MiB; exceeding this limit sends WebSocket close code 1009. The timeout for each write operation is 10 seconds; session termination allows up to 5 seconds each to wait for the read and send goroutines to exit.

Streaming endpoints uniformly use `text/event-stream`, with each SSE frame terminated by an empty line:

| Protocol | Initial Event | Content Sequence | usage | Termination Event |
| --- | --- | --- | --- | --- |
| OpenAI Chat | assistant role chunk | chat completion delta | Located after finish chunk when `include_usage=true` | `data: [DONE]` |
| OpenAI Responses | `response.created`, `response.in_progress` | output item / content part / delta / done | `usage` of completed response | `response.completed` or `response.incomplete` |
| Anthropic | `message_start` | `content_block_start`, delta, `content_block_stop` | `message_delta.usage` | `message_stop` |
| Gemini | candidate Part | `GenerateContentResponse` deltas | `usageMetadata` in the final frame | Finish reason in the final frame |

When the interval between upstream semantic events reaches 10 seconds, all four streaming protocols send an SSE comment frame `: ping` and flush immediately. OpenAI Chat sends the assistant role chunk first, Responses sends `response.created` and `response.in_progress` first, and Anthropic sends `message_start` first; these initial events can reach the client during account scheduling. Gemini's first frame originates from an upstream semantic event or `: ping`.

Public adaptation rules:

| Canonical Event | OpenAI Chat | Responses | Anthropic | Gemini |
| --- | --- | --- | --- | --- |
| text | message/content delta | output_text | text block | candidate text Part |
| reasoning | `reasoning_content` | reasoning summary | thinking block | thought Part |
| function | `tool_calls` | function_call item | tool_use block | functionCall Part |
| function result | tool message | function_call_output | tool_result | functionResponse Part |
| code execution | readable Markdown | code_interpreter item | text block | executableCode/result Part |
| grounding/citation | annotations | output annotations | text sources | groundingMetadata |
| media | data URL / media endpoint | output content | content block | inlineData Part |
| usage | prompt/completion/total | input/output/total | input/output | prompt/candidates/thoughts/total |

Responses media uses image generation item objects.

OpenAI Chat carries generated images via Markdown data URLs; when the client passes assistant `message.content` back in the next turn, the adapter restores images within it to inline data Parts, preserving multi-turn image context. Image Base64 supports standard and URL-safe alphabets, optional padding, and CR/LF line breaks; text before and after images maintains its original order.

In user text, `youtu.be/<ID>`, `youtube.com/watch?v=<ID>`, `/shorts/<ID>`, `/live/<ID>`, and `/embed/<ID>` are converted into `video/*` external media parts and removed from the user text part; duplicate URLs are deduplicated into a single attachment. OpenAI `video_url`/`input_video`, Anthropic URL sources, and Gemini `fileData.fileUri` use the same external media encoding.

OpenAI Responses `previous_response_id` and Interactions `previous_interaction_id` maintain up to 256 in-process nodes to reconstruct conversation history; upon server restart clients submit full context. Drive and Veo bindings persist to disk.

Model, parameter, account, and upstream errors are projected according to the status table below. Client cancellation closes the upstream reader and releases the account lease.

Error objects and status semantics:

| Condition | HTTP | OpenAI | Anthropic | Gemini |
| --- | ---: | --- | --- | --- |
| Invalid parameters, schema, or tool choice | 400 | `invalid_request` | `invalid_request_error` | `INVALID_ARGUMENT` |
| No eligible accounts | 400 | `account_required` | `invalid_request_error` | `INVALID_ARGUMENT` |
| All accounts supporting the request are unschedulable | 503 | `account_unavailable` | `api_error` | `UNAVAILABLE` |
| Invalid local API key | 401 | `invalid_api_key` | `authentication_error` | `UNAUTHENTICATED` |
| Model or method not found | 404 | `model_not_found` | `not_found_error` | `NOT_FOUND` |
| Local file not found | 404 | `file_not_found` | `not_found_error` | `NOT_FOUND` |
| Upstream permission denied | 403 | `upstream_error` | `permission_error` | `PERMISSION_DENIED` |
| Video still generating | 409 | `video_not_ready` | `api_error` | `INTERNAL` |
| File exceeds 512 MiB | 413 | `file_too_large` | `request_too_large` | `INTERNAL` |
| Upstream quota or rate limited | 429 | `upstream_error` | `rate_limit_error` | `RESOURCE_EXHAUSTED` |
| All candidate accounts in cooldown and will not recover within 1 minute | 429 | `rate_limit_exceeded` | `rate_limit_error` | `RESOURCE_EXHAUSTED` |
| Upstream overloaded | 529 | `upstream_error` | `overloaded_error` | `INTERNAL` |
| Current client request canceled by administrator | 503 | `request_canceled` | `api_error` | `UNAVAILABLE` |
| Generation service stopped | 503 | `service_stopped` | `api_error` | `UNAVAILABLE` |
| Request deadline exceeded | 504 | `upstream_error` | `api_error` | `DEADLINE_EXCEEDED` |
| Transport, Content-Type, decoding, or missing terminal state | 502 | `upstream_error` | `api_error` | `INTERNAL` |

- "no eligible account": no account matches the requested method, capabilities, and benefit tiers
- "accounts supporting request cannot be scheduled": matching accounts require re-login, are unavailable, or disabled
- Generation service `STOPPED`: generation and token counting endpoints return `service_stopped`

Upstream HTTP 404 maps to 502 Bad Gateway; public 404 indicates missing local models or resources. Client disconnects record 499 in access logs.

Error object raw body:

**OpenAI Chat / Responses**

```json
{
  "error": {
    "message": "upstream response ended before finish frame",
    "type": "api_error",
    "code": "upstream_error"
  }
}
```

**Anthropic**

```json
{
  "type": "error",
  "error": {
    "type": "api_error",
    "message": "upstream response ended before finish frame"
  }
}
```

**Gemini**

```json
{
  "error": {
    "code": 502,
    "message": "upstream response ended before finish frame",
    "status": "INTERNAL"
  }
}
```

Terminal raw text after a streaming response has started:

```text
# OpenAI Chat
data: {"error":{"message":"...","type":"api_error","code":"upstream_error"}}

# OpenAI Responses
event: response.failed
data: {"response":{"id":"resp_...","object":"response","status":"failed","error":{"code":"upstream_error","message":"..."}}}

# Anthropic
event: error
data: {"type":"error","error":{"type":"api_error","message":"..."}}

# Gemini
data: {"error":{"code":502,"message":"...","status":"INTERNAL"}}
```

MakerSuite error parsing:

| Source | Path | Public Result |
| --- | --- | --- |
| HTTP status | response status | Preserves original status code |
| protocol code | `$[1][0]` | Maps to protocol error code/type/status |
| protocol message | `$[1][1]` | Written to public error `message` |
| Raw shape | `[null,[code,message,...]]` | Parsed into canonical error event |

Request lifecycle:

| Phase | HTTP / SSE Behavior | Resource State |
| --- | --- | --- |
| Failure before response headers | Returns corresponding HTTP status and protocol JSON error | Releases account slot |
| Failure after SSE has started | Sends OpenAI error, `response.failed`, Anthropic `error`, or Gemini error frame | Closes upstream reader and releases account slot |
| Completion frame | Outputs finish reason, usage, and protocol termination event | Merges Set-Cookie and releases account slot |
| Client cancellation | Terminates upstream read | Cancels request context and releases account slot |

Secondary development is welcome. If this project helps you, consider giving the repository a star!
