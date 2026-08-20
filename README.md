# ultra-zen

[![Go](https://img.shields.io/github/go-mod/go-version/raketenkater/ultra-zen)](https://go.dev/)
[![CI](https://github.com/raketenkater/ultra-zen/actions/workflows/ci.yml/badge.svg)](https://github.com/raketenkater/ultra-zen/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/raketenkater/ultra-zen)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/raketenkater/ultra-zen)](https://goreportcard.com/report/github.com/raketenkater/ultra-zen)

Run Claude Code — including Ultracode workflows — on free and low-cost models.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="ultra-zen-selector.png">
  <img alt="ultra-zen model selector" src="ultra-zen-selector.png" width="660">
</picture>

Run **Claude Code** on **opencode Zen models** (the full `opencode-go` tier:
glm, kimi, minimax, qwen, deepseek, grok, mimo, hy3-preview, plus the `*-free`
models), **OpenRouter free models**, or a **Codex endpoint** backed by a ChatGPT
subscription — with full Claude Code feature parity (Ultracode workflows,
subagents, MCP, hooks, WebFetch, tools, streaming).

The Ultracode workflow engine — Claude Code's multi-agent, deep-reasoning mode —
is the reason ultra-zen exists: it works end to end on free models, with a hook
that keeps background fan-out alive on slow gateways.

`ultra-zen` is a launch wrapper around Claude Code. It reads your opencode API
key (or an OpenRouter/Codex key), lists available models, lets you pick one from
a searchable TUI, starts a local **Anthropic → OpenAI translation proxy**, and
execs `claude` pointed at it. Claude Code only speaks the Anthropic Messages API;
a thin bridge translates to the OpenAI Chat Completions format the upstream
gateways serve.

```
ultra-zen
  ├─ read API key (auth.json or OPENROUTER_API_KEY)
  ├─ fetch live model list
  ├─ TUI model selector
  ├─ start proxy (goroutine) 127.0.0.1:<free port>
  │     Anthropic /v1/messages  →  OpenAI /chat/completions
  │     /v1/models advertises available ids (subagent model probe)
  │     tools · streaming SSE · reasoning_content handling
  ├─ build claude env (stripped auth, local base URL, tier mapped)
  ├─ inject --settings (Workflow stallMs hook) + DDG MCP for web research
  └─ exec claude "$@"   (proxy torn down on exit)
```

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/raketenkater/ultra-zen/master/install.sh | sh
```

Or with Go:

```bash
go install github.com/raketenkater/ultra-zen/cmd/ultra-zen@latest
```

From source:

```bash
git clone https://github.com/raketenkater/ultra-zen
cd ultra-zen
make build
```

### Prerequisites

- **Claude Code** on `PATH` (`npm i -g @anthropic-ai/claude-code`).
- **opencode** logged in with the `opencode-go` provider:
  `opencode auth login` → select the opencode provider. The API key is read
  from `~/.local/share/opencode/auth.json` (or `~/.config/opencode/auth.json`).
- **OpenRouter API key** (for `--provider openrouter`):
  get one at [openrouter.ai/keys](https://openrouter.ai/keys).
- **uvx** (optional) for web research — when present, `ultra-zen` wires a
  no-key DuckDuckGo MCP and disables the Anthropic-only `WebSearch` tool.

### API keys

Every provider that needs a key is resolved in this order:

1. **Flag / env var** — `--api-key`, `--openrouter-key`, or the provider's env
   var (`OPENROUTER_API_KEY`, `MODELSCOPE_API_KEY`, `GROQ_API_KEY`, ...).
2. **The stored key** — `~/.config/ultra-zen/keys/<provider>` (one file per
   provider, mode `0600`). Use the TUI's key manager (press `k` in the
   selector) or write the file directly.
3. **The system-wide stored key** — `/etc/ultra-zen/keys/<provider>` (mode
   `0644`, set up by `ultra-zen setup`), so a shared credential covers users
   with no personal key.
4. **Interactive prompt** — when running interactively with no key set, the
   TUI asks you to paste one, then **saves it to the key store** so you only
   enter it once.

Precedence is `flag/env` → `stored (user)` → `stored (system)` → `prompt`, so
an env var always wins over a stored key, and a per-user key always wins over a
shared system key (any user can opt out of the shared credential for their own
sessions). Clearing a stored key (empty string) makes the prompt appear again.

### System-wide access (`uz`)

`ultra-zen setup` installs the binary plus a `uz` symlink so the launcher is on
`PATH` from any directory for any user on the machine, and initialises the
shared key store at `/etc/ultra-zen/keys`:

```bash
sudo ultra-zen setup --copy-keys      # install to /usr/local/bin, share your keys
uz --list                             # works from any directory, any user
uz glm-5.1 -- --resume                # passthrough args, Claude Code stays in the cwd you launched from
```

- `uz` is a symlink to the same `ultra-zen` binary; nothing behaves differently
  under either name. Claude Code is launched in the directory you ran it from
  (ultra-zen never changes the working directory).
- The system store is **world-readable (0644)** — any local user can read the
  shared keys. This is the deliberate trade-off of "any user on the machine":
  it is only appropriate for a trusted workstation. Prefer the per-user store
  when you need a personal key that others can't read (a per-user key wins).
- Writes to the system store need root: `sudo ultra-zen keys --system set
  <provider> <key>`, `sudo ultra-zen keys --system clear <provider>`. The
  `ULTRA_ZEN_SYSTEM_KEYS` env var redirects the store (mostly for testing).
- `make system` and `install.sh` (when installing to `/usr/local/bin` via
  sudo) perform the same setup.

## Usage

```bash
ultra-zen                                    # TUI selector → opencode Zen
ultra-zen --provider openrouter              # OpenRouter free models
ultra-zen glm-5.1                            # skip the selector
ultra-zen kimi-k2.6 -- --resume              # args through to claude
ultra-zen --list                             # list models and exit
ultra-zen --list --provider openrouter       # list OpenRouter free models
ultra-zen --proxy-only glm-5.1               # proxy only (debugging)
ultra-zen --port 8787 glm-5.1                # pin a port
ultra-zen --version                          # print version

# Resilient cross-provider session — first route is primary
ultra-zen --free-model openrouter:qwen/qwen3-coder:free \
          --free-model opencode:deepseek-v4-flash-free \
          --free-model openrouter:openrouter/free

# Zen primary with a cross-provider OpenRouter escape hatch
ultra-zen --free-model openrouter/free laguna-s-2.1-free

# Legacy orchestrator/worker split
ultra-zen glm-5.1 --worker mini-max-m2.5
```

Flags may come before or after the model argument. Put `--` before arguments
that must be passed through unchanged to Claude Code.

The default TUI is a complete launcher. Its first screen shows every model
from the primary provider immediately, then adds models from every other
configured free provider as their live catalogs load. Selecting one switches
the backend automatically; `--provider` is not required. The first row is
**Free cycle**: Enter configures its ordered routes, Esc saves the pool, and
Enter on the now-ready Free cycle row launches from its first route. Use `f`
to edit the pool and `k` to add provider keys at any time. The ordered cycle is
stored at `~/.config/ultra-zen/free-pool.json`, restored after a restart, and
also applies to direct `ultra-zen <model>` launches. An explicit
`--free-model` or `--worker` flag overrides the saved cycle.

### Backends

**opencode Zen** (default, `--provider opencode-go`): reads your opencode API
key from `auth.json`. Lists `opencode-go` tier models and `*-free` models.

**OpenRouter** (`--provider openrouter`): reads `OPENROUTER_API_KEY` from the
environment. Lists `:free` models, including `qwen/qwen3-coder:free`,
`deepseek/deepseek-chat:free`, `google/gemini-2.5-flash:free`, and
`openrouter/free` (auto-routes to the best available free model).

**GWDG SAIA** (`--provider saia`): uses the OpenAI-compatible Academic Cloud
endpoint at `https://chat-ai.academiccloud.de/v1`. Set `SAIA_API_KEY`, or store
the key once with `ultra-zen keys set saia -` and provide it on stdin. Models
are discovered live from SAIA; `qwen3-coder-next` is the recommended coding
model.

OpenRouter currently allows **50 free-model requests per day** on a free
account. After the account has purchased at least **$10 in credits**, that
allowance becomes **1,000 free-model requests per day**. The credits remain
available for paid models; this is a purchase threshold, not a requirement to
spend $10 on free requests. See OpenRouter's current
[rate-limit FAQ](https://openrouter.ai/docs/faq). The free tier is also limited
to 20 requests/minute; ultra-zen therefore paces one session to 20 RPM by
default (`--openrouter-rpm 0` disables this).

**Codex / ChatGPT subscription** (`--provider codex`): two backends, one flag.

1. **ChatGPT subscription (auto-detected)** — if you have the
   [codex CLI](https://github.com/openai/codex) installed and logged in with a
   ChatGPT Plus/Pro account, ultra-zen detects everything from
   `~/.codex/auth.json` and talks directly to the ChatGPT backend (the OpenAI
   Responses API). No URL, no key, no ChatMock:

   ```bash
   ultra-zen --provider codex          # auto-detect → pick a GPT model
   ultra-zen --list --provider codex   # GPT-5.6-sol, GPT-5.5, GPT-5.4, …
   ```

   The model catalog is fetched live from `chatgpt.com/backend-api/codex/models`
   with the account's bearer token, falling back to the codex CLI's own
   `models_cache.json` if the endpoint is unreachable. Expiring access tokens
   are refreshed automatically through the same OAuth flow the codex CLI uses,
   and `auth.json` is rewritten atomically so the CLI stays in sync. The TUI's
   start screen shows a **Codex (ChatGPT sub)** row whenever the login is
   detected.

2. **Local endpoint** — point at an OpenAI-compatible endpoint backed by a
   ChatGPT login, e.g. [ChatMock](https://github.com/RayBytes/ChatMock), which
   serves GPT-5 through the Codex OAuth client without an OpenAI API key:

   ```bash
   export CODEX_BASE_URL=http://127.0.0.1:8000/v1
   ultra-zen --provider codex
   ```

When run interactively with neither a codex login nor a URL set, ultra-zen
prompts for the endpoint URL. The explicit local endpoint always wins over
auto-detection, so `CODEX_BASE_URL` / `--codex-url` are authoritative when set.

**Other free-tier providers** (`--provider groq|cerebras|huggingface|cohere|modelscope`):
BYO-key OpenAI-compatible endpoints with their own free tiers, beyond opencode
Zen. Each needs its own personal API key — set the provider's env var or pass
`--api-key`:

| `--provider`  | Env var            | Get a key                                           |
|---------------|---------------------|-----------------------------------------------------|
| `groq`        | `GROQ_API_KEY`      | https://console.groq.com/keys                       |
| `cerebras`    | `CEREBRAS_API_KEY`  | https://cloud.cerebras.ai/platform/apikeys           |
| `huggingface` | `HF_TOKEN`          | https://huggingface.co/settings/tokens               |
| `cohere`      | `COHERE_API_KEY`    | https://dashboard.cohere.com/api-keys                |
| `modelscope`  | `MODELSCOPE_API_KEY` | https://modelscope.ai/my/myaccesstoken               |

```bash
ultra-zen --provider groq
ultra-zen --provider huggingface --api-key hf_xxx
ultra-zen --provider modelscope                # Alibaba ModelScope free tier
```

ModelScope's international (`.ai`) and China (`.cn`) sites use separate
accounts and tokens. Ultra-zen detects both endpoints automatically. An
international account must also be linked at
https://modelscope.ai/my/settings/account before API-Inference can be used.
ModelScope sometimes advertises account-gated models without an access flag.
If inference explicitly returns “does not have access to this model”,
ultra-zen retires that route, continues on the next provider, and records the
denial in `~/.config/ultra-zen/unavailable-models.json` so the model is hidden
from later TUI/catalog loads. Replacing the provider key clears those cached
denials and checks the new account afresh.

ModelScope's API-Inference serves open-source models (DeepSeek-V4, GLM-5.x,
Qwen 3.5, MiniMax) free of charge, but it is a non-commercial, non-profit
product: quotas are ~2,000 calls/day total per account with a ~500
calls/day per-model cap, and it is meant for development and evaluation, not
production traffic. See the [usage limits
docs](https://modelscope.ai/docs/model-service/API-Inference/limits).

Sourced from the community-curated free-tier list at
[cheahjs/free-llm-api-resources](https://github.com/cheahjs/free-llm-api-resources),
restricted to providers that actually serve `GET /models` in OpenAI's format
(a few well-known free tiers, e.g. Gemini's OpenAI-compat layer, don't
implement model listing and so aren't wired up here). Free-tier limits change
often — check the provider's own dashboard if requests start getting
rate-limited.

### Session resume

ultra-zen records the Claude Code session ID behind every launch, so an
interrupted Ultracode workflow run can be reopened later instead of starting
over:

```bash
ultra-zen sessions          # list recorded sessions for this directory
ultra-zen resume            # reopen the newest one, replaying its launch
ultra-zen resume latest --provider openrouter   # reopen, overriding a flag
```

On resume, ultra-zen looks up the session's newest workflow run in Claude
Code's own transcript state, reports how many agents will replay from cache,
and asks Claude to call `Workflow({ resumeFromRunId: ... })` as its opening
turn — cached agents skip a model call; anything still in flight when the
session stopped re-runs.

Interactive launches record the finalized TUI model, provider, worker/pool,
and relevant pacing/port flags. Selecting the TUI's Resume row therefore
reopens the recorded provider directly instead of opening another picker or
falling back to the default provider.

### Resilient free-model pool

`--free-model <provider:model>` creates an ordered, cross-provider free-model
pool. Use `openrouter:<id>` or `opencode:<id>`, repeat the flag, or pass a
comma-separated list. A bare ID remains shorthand for OpenRouter. The first
model is the primary when no positional model is supplied; otherwise the
positional model stays primary and the `--free-model` entries are its
fallbacks.

In the interactive selector, choose the first **Free cycle** row (or press
`f`) to build the same ordered pool. Enter toggles a model, `r` clears the
pool, and Esc saves and returns. Press Enter on **Free cycle ready** to launch.
The pool remains selected if you reopen the screen and replaces a combo's
legacy worker model. It is saved on Esc, restored on the next launch, and used
for both TUI and direct model launches. Press `r` then Esc to clear the saved
cycle. Press `k` to add or change provider keys without leaving the selector.

```bash
# Explicit OpenRouter + opencode Zen provider pool (no positional model needed)
ultra-zen --free-model openrouter:qwen/qwen3-coder:free \
          --free-model opencode:deepseek-v4-flash-free \
          --free-model openrouter:openrouter/free

# Keep a Zen free model first, then leave that provider when its allocation ends
ultra-zen --free-model openrouter:qwen/qwen3-coder:free \
          --free-model openrouter:openrouter/free \
          laguna-s-2.1-free
```

The pool replaces the worker/thinker split for that session: main-loop,
background-agent, and fast-tier requests all share the same active route.
Daily/free-allocation exhaustion is provider-wide: OpenRouter's
`free-models-per-day` opens the circuit for every OpenRouter free route, while
Zen's `FreeUsageLimitError` and another provider's `insufficient_quota` open it
for every route using that provider/account. The
interrupted request is replayed immediately on the other provider, with its
full Claude Code conversation intact. Temporary per-model `429` responses can
still try a sibling model, honor `Retry-After` when present, and otherwise use
exponential backoff. Once a fallback succeeds it becomes the first route for
subsequent requests.

Without an explicit pool, ultra-zen discovers both providers from credentials
already present: `OPENROUTER_API_KEY` supplies `openrouter/free`, and the
opencode `auth.json` supplies its live `*-free` model list. Selecting a free
model on either provider therefore adds the other provider automatically when
both credentials are configured, without another prompt.

Model rotation cannot bypass OpenRouter's account-wide daily allowance: after
the account's 50/1,000 free requests are consumed, every OpenRouter free model
will reject requests, so ultra-zen switches to opencode rather than trying more
OpenRouter models under the same exhausted account. Purchasing $10 of credits
is the supported way to raise OpenRouter's account-wide free allowance.

### Legacy orchestrator / worker split

`--worker <model>` runs two models behind one proxy. The main Claude Code loop
uses the orchestrator model; background sub-agents spawned by Ultracode
workflows use the worker model.

The proxy classifies requests by inspecting the tool list. The main loop
carries interactive tools (`AskUserQuestion`, `Skill`, `EnterPlanMode`) that
sub-agents never see. A request with those tools goes to the orchestrator;
everything else goes to the worker.

```bash
ultra-zen glm-5.1 --worker mini-max-m2.5       # go tier
ultra-zen kimi-k3 --worker deepseek-v4-flash-free  # free worker
```

`--worker` remains available for existing paid/cheap two-model setups, but it
cannot be combined with an explicit `--free-model` pool.

## How it works

### Model selection
`ultra-zen` fetches the live model list from the active backend:

- **opencode-go tier** (`https://opencode.ai/zen/go/v1`) — models the
  `opencode-go` key can access.
- **free tier** (`https://opencode.ai/zen/v1`) — `*-free` models.
- **OpenRouter** (`https://openrouter.ai/api/v1`) — `:free` models and
  `openrouter/free`.
- **Codex / ChatGPT subscription** (`https://chatgpt.com/backend-api/codex`) —
  the auto-detected backend's `GET /models` catalog (falling back to the codex
  CLI's `models_cache.json`), listing the models a ChatGPT Plus/Pro account can
  serve.

Free pools keep the selected primary plus ordered OpenRouter routes in the
local proxy. The proxy rotates only on rate-limit responses; ordinary invalid
requests still surface as errors instead of silently changing models.

### Translation proxy
The proxy (`internal/proxy`) translates both directions:

| Anthropic (Claude Code)          | OpenAI (upstream)               |
|----------------------------------|----------------------------------|
| `system` (string or blocks)      | `messages[0]` role `system`      |
| `messages[].content` blocks      | `content` / `tool_calls` / `tool` |
| `tools[].input_schema`           | `tools[].function.parameters`    |
| `tool_choice`                    | `tool_choice`                    |
| `stop_reason`                    | `finish_reason`                  |
| SSE `message_start`…`message_stop` | SSE `data:` chunks             |

For the auto-detected **ChatGPT subscription** backend, the proxy additionally
bridges to the **OpenAI Responses API**: chat-completions requests are
translated to `{model, input, instructions, tools, stream}` (the codex backend
requires `stream:true` and rejects `store`/`max_output_tokens`), and the
Responses SSE stream (`response.output_text.delta`, `response.output_item.done`,
`response.completed`) is folded back into the chat-completions chunks the rest
of the pipeline already handles. So the same battle-tested Anthropic↔OpenAI
translation — tool-call repair, phantom-block fixes, reasoning folding — applies
unchanged to ChatGPT models.

The proxy advertises the full launch-provider model catalog at `/v1/models`, so
Claude Code's `/model` command lists every model from the active provider (not
just the selected one) and switches between them live. Two things make that
work:

- **Gateway discovery is enabled** — ultra-zen injects
  `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`, which makes Claude Code fetch
  `/v1/models` from the proxy (verified against the installed Claude Code
  binary). The `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL` override is
  deliberately NOT set: it would tell Claude Code the proxy IS first-party
  Anthropic, which disables discovery.
- **The catalog is pre-cached** — ultra-zen writes Claude Code's
  `gateway-models.json` cache file directly before launch, so the `/model`
  picker populates instantly without depending on Claude Code's own
  startup-race-prone discovery fetch.
- **Advertised ids survive the picker filter** — Claude Code's `/model`
  gateway discovery drops any id that doesn't match `/(claude|anthropic)/i`, so
  the proxy advertises every model under a `claude-<provider>-<model>` id (e.g.
  `claude-codex-gpt-5.6-sol`) and routes it back to the real upstream model.
  The display name stays the human model name.

Reasoning models emit their answer in `reasoning_content`; the proxy surfaces
it as a text block so Claude Code never sees an empty message.

### Claude Code environment
The launcher sets:

- `ANTHROPIC_BASE_URL` → the local proxy
- `ANTHROPIC_AUTH_TOKEN=ultra-zen`, `ANTHROPIC_MODEL` + all tier vars → selected model
- Watchdog/timeouts relaxed so a remote model never trips idle timers
- `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE=85` (compact before the proxy's emergency context rescue)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="ultra-zen-launch.png">
  <img alt="ultra-zen launched with claude" src="ultra-zen-launch.png" width="660">
</picture>

### Ultracode / Workflow support
A `PreToolUse` hook rewrites Workflow `agent()` scripts to set `stallMs` to
the maximum safe value, so background fan-out never aborts a quiet model.

Ultracode is on by default, and every session starts at the highest thinking
budget (`--effort max`): the injected `--settings` payload carries
`"ultracode": true` and `"effortLevel": "max"`. Your own `--effort` flag always
wins.

### Web research
The Anthropic-only `WebSearch` tool cannot run against the local proxy, so
when `uvx` is present it is disabled and replaced with a no-key DuckDuckGo MCP
(`mcp__ddg-search__search`, `mcp__ddg-search__fetch_content`). The three flags
(`--disallowedTools WebSearch`, `--mcp-config`, `--allowedTools`) are emitted
together as one block, so without `uvx` the built-in `WebSearch` is left
untouched rather than disabling web research entirely. Your own
`--disallowedTools` / `--mcp-config` / `--allowedTools` args always win.

## Project layout

```
cmd/ultra-zen/      entry point: selector + launcher + provider dispatch
internal/auth/      opencode auth.json reader (or env for OpenRouter)
internal/models/    live model list from Zen gateway or OpenRouter
internal/proxy/     Anthropic↔OpenAI translation (request/response/stream)
internal/claude/    env build + settings injection + exec
internal/tui/       Bubble Tea model selector
internal/workflow/  Workflow script rewriter (stallMs injection hook)
```

## License

MIT
