# Ultra-zen: Codex subscription auto-detect + device model list

**Branch:** `feat/recheck-denied-models` (current).

## Quest

Make ultra-zen use the **Codex/ChatGPT subscription** it finds on this machine,
auto-detecting everything it needs from the installed device — and make Claude
Code's `/model` list show the models ultra-zen launches through it.

## Verified device state (auto-detect source)

- `codex` CLI **0.147.0** at `~/.nvm/.../bin/codex` (npm), **ChatGPT Plus** login.
- `~/.codex/auth.json`: `auth_mode:"chatgpt"`, `tokens.access_token` (JWT,
  ~10-day exp), `tokens.refresh_token`, `tokens.account_id`,
  `chatgpt_plan_type:"plus"`.
- `~/.codex/config.toml`: `model="gpt-5.6-sol"`, `model_reasoning_effort="medium"`.
- `~/.codex/models_cache.json`: the codex CLI's own cached model list — 6
  `visibility:"list"` models for Plus: `gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna,
  gpt-5.5, gpt-5.4, gpt-5.4-mini`. (`gpt-5.3-codex` retired.)
- `CODEX_BASE_URL` / `CODEX_API_KEY`: **not set** in env. No ChatMock.

## ⚠️ Adversarial verification corrections (adopted)

The verification workflow (3 completed agents + 1 security agent, stopped when
it had gone sufficiently deep) confirmed the approach but **refuted two of my
initial premises**. The plan below reflects the corrected reality:

1. **The ChatGPT backend is the Responses API, NOT chat/completions.**
   - Base `https://chatgpt.com/backend-api/codex` (no `/v1` segment).
   - Inference: `POST {base}/responses` — request body is `{model, input,
     instructions, tools, tool_choice, reasoning, store:false, stream:true}`,
     NOT `{messages, max_tokens, temperature}`.
   - Models: `GET {base}/models?client_version=X` — response shape is
     `{"models":[{slug, display_name, context_window, visibility,
     supported_in_api}]}`, NOT OpenAI `{"data":[{id}]}`.
   - Streaming: Responses SSE events (`event: response.output_text.delta`,
     `response.function_call_arguments.delta`, `response.completed`), NOT
     `data: {"choices":[{"delta":{...}}]}` chunks.
   - The **existing proxy POSTs `BaseURL+"/chat/completions"` (proxy.go:801) →
     404 on this backend.** So "reuse the proxy unchanged" does NOT hold. A new
     translation path is required.

2. **Claude Code's /model picker has NO `label:` group-separator support.**
   Verified against the installed Claude Code v2.1.226 binary: no label:
   separator mechanism exists; the only non-selectable flag is
   `disabled: true`; gateway-discovery `/v1/models` fetch is feature-gated
   (`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`) and filters ids with
   `/(claude|anthropic)/i`. So provider-group headers should be **real,
   selectable, routing-neutral model ids** (not fake separators).

3. **Auth is `Authorization: Bearer <access_token>` + `ChatGPT-Account-ID:
   <account_id>`** (both from auth.json). This is what Simon Willison's
   official-sanctioned `llm-openai-via-codex` and David-Factor's Go proxy use.
   The codex CLI's own AgentAssertion ed25519 bootstrap is NOT needed for this
   path. Access tokens expire (~10 days); near-expiry/expired tokens are
   rejected → **implement refresh** via
   `POST https://auth.openai.com/oauth/token` (`grant_type=refresh_token`,
   `client_id=app_EMoamEEZ73f0CkXaXp7hrann`) and atomically rewrite auth.json.

## Architecture — two providers in one `codex` namespace

- **`codex` = local endpoint** (existing, unchanged): user sets
  `--codex-url` / `CODEX_BASE_URL`. Prompt for URL when interactive + nothing set.
- **`codex-sub` = ChatGPT subscription** (new, auto-detected): talks to the
  ChatGPT backend's **Responses API** using auth.json. Completely separate
  translation path.

### Detection order (main.go codex branch)

1. `--codex-url` / `CODEX_BASE_URL` set → **local endpoint** (existing path).
2. Else `~/.codex/auth.json` readable with `auth_mode:"chatgpt"` + non-empty
   access_token + account_id → **codex-sub**: base `CodexSubBase`, apiKey =
   access_token, plus account_id for the header.
3. Else if interactive → prompt for base URL (existing).
4. Else → die with the existing message.

### Codex-sub request/response translation (the real work)

The proxy already translates Anthropic ⇄ OpenAI **chat-completions**. For
codex-sub, the upstream speaks the **Responses API**. Two options per the
verifiers:

- **(a)** Add a Responses-API client inside the proxy: translate
  Anthropic ⇄ Responses directly (new request.go/response.go/stream.go paths).
- **(b)** Reuse an existing Responses⇄Chat-Completions shim as the codex-sub
  upstream, keeping the proxy's chat-completions translation unchanged.

**Chosen: (b) — implement a small in-process Responses⇄chat-completions adapter
as a separate upstream type in the proxy.** Rationale:
- The proxy's Anthropic⇄chat-completions translation is battle-tested against
  Claude Code (tool-call repair, phantom-block fixes, reasoning folding).
  Reusing it for codex-sub means all that hardening applies to ChatGPT models.
- Only the upstream-facing half changes: POST to `{base}/responses`, translate
  chat-completions request → Responses request, Responses SSE → chat-completions
  SSE chunks. This is a bounded, well-understood delta (both formats are OpenAI
  Shapes; Responses is a superset).
- `llm-openai-via-codex` + David-Factor's proxy prove the adapter shape works.

### The /model list (both asks)

- `ListCodexSub()` parses `{"models":[{slug,display_name,context_window}]}` →
  `[]models.Model` (Free:false). Source: live `GET {base}/models?client_version=`
  with `~/.codex/models_cache.json` as local fallback.
- `modelInfos` → the proxy's `/v1/models` gains provider-group headers: real
  selectable ids (e.g. `gpt-5.6-sol`) with a `disabled:true` non-selectable
  header whose id contains "claude" (so it survives the gateway filter) when a
  group separator is needed. **No `label:` entries** — verified unsupported.

## Implementation files

- `internal/codex/codex.go` (new) — `Auth()` (read + validate auth.json),
  `Refresh()` (OAuth token refresh, atomic rewrite 0600), `PrimaryModel()`
  (config.toml). Tests with temp `CODEX_HOME`.
- `internal/models/models.go` — `CodexSubBase`, `ListCodexSub()`, models-cache
  fallback parser, `BaseForProvider("codex")`. Tests.
- `internal/proxy/responses.go` (new) — the Responses⇄chat-completions adapter
  (request translate + SSE translate). `Config`/`Upstream` gain a `Kind`
  (`chat` vs `responses`) so `forwardTo` picks the right path. Tests.
- `internal/proxy/proxy.go` — `forwardTo` branch on Kind; `/v1/models` groups.
- `internal/tui/fallbacks.go` — codex-sub row (auto-detect, list models).
- `cmd/ultra-zen/main.go` — codex branch auto-detect; pass Kind/account-id;
  `/v1/models` grouping.
- `README.md` — codex-sub section.

## Sequencing

1. `internal/codex` package + tests (no deps).
2. `ListCodexSub` + `BaseForProvider` + models tests.
3. `internal/proxy/responses.go` adapter + tests (the core new logic).
4. `/v1/models` grouping + tests.
5. TUI codex-sub row.
6. main.go integration.
7. `make build` + `go test ./...` green; manual `--list --provider codex`.
8. Commit.

## Verification

```bash
make build && go test ./...
ultra-zen --list --provider codex   # codex-sub models from ChatGPT backend
ultra-zen --list                    # codex-sub row present via auto-detect
# launch claude; /model shows the codex catalog (real selectable ids)
```
