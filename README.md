# ultra-zen

[![Go](https://img.shields.io/github/go-mod/go-version/raketenkater/ultra-zen)](https://go.dev/)
[![CI](https://github.com/raketenkater/ultra-zen/actions/workflows/ci.yml/badge.svg)](https://github.com/raketenkater/ultra-zen/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/raketenkater/ultra-zen)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/raketenkater/ultra-zen)](https://goreportcard.com/report/github.com/raketenkater/ultra-zen)

**Run Claude Code UltraCode workflows on free & cheap models — no Anthropic bill.**

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="ultra-zen-selector.png">
  <img alt="ultra-zen model selector" src="ultra-zen-selector.png" width="660">
</picture>

> 💰 **Claude Max UltraCode = $100–200/month. ultra-zen = $0 (free tier) or $10/month (Go tier).**
> Same workflows. Same multi-agent fan-out. 10–20× cheaper.

`ultra-zen` is a **single Go binary** that wraps Claude Code and makes it speak
to two free/cheap model backends:

- **[opencode Zen](https://opencode.ai/zen)** — `*-free` models and the full
  `opencode-go` tier (glm, kimi, minimax, qwen, deepseek, grok, mimo, hy3)
- **[OpenRouter](https://openrouter.ai)** — any `:free` model or the
  `openrouter/free` auto-router

It reads your opencode API key (or OpenRouter key from the environment), lists
available models, lets you pick one from a searchable TUI, starts a local
**Anthropic → OpenAI translation proxy**, and execs `claude` pointed at it. Full
UltraCode feature parity: multi-agent workflows, subagents, MCP, hooks,
streaming, web research — all on free/cheap models.

```
ultra-zen                          # opencode Zen
ultra-zen --provider openrouter    # OpenRouter free models
  ├─ read API key (auth.json or env)
  ├─ fetch live model list
  ├─ TUI model selector
  ├─ start proxy (goroutine) 127.0.0.1:<free port>
  │     Anthropic /v1/messages  →  OpenAI /chat/completions
  │     /v1/models advertises the selected id (subagent model probe)
  │     tools · streaming SSE · reasoning_content handling
  ├─ build claude env (stripped auth, local base URL, tier mapped)
  ├─ inject --settings (Workflow stallMs hook) + DDG MCP for web research
  └─ exec claude "$@"   (proxy torn down on exit)
```

## Why ultra-zen?

Most tools that route Claude Code to non-Anthropic backends assume you already
have paid API keys. ultra-zen is purpose-built for **free and low-cost
models** — with zero-config for opencode users.

|  | Claude Max | claude-code-proxy | UltraCode-Shim | **ultra-zen** |
|---|---|---|---|---|
| **Cost** | $100–200/mo | Your API keys | Your API keys | **Free / $10/mo Go** |
| **Setup** | Just Claude | Python + config | Python + config | **Single binary, zero config** |
| **Free models** | ❌ | ❌ | ❌ | **✅ (Zen + OpenRouter)** |
| **UltraCode** | ✅ | ❌ | ✅ | **✅ (stallMs hook)** |
| **Orch/worker split** | ❌ | ❌ | ✅ | **✅** |
| **TUI selector** | ❌ | ❌ | ❌ | **✅** |
| **DDG web research** | ❌ | ❌ | ❌ | **✅** |
| **OpenRouter** | ❌ | ❌ | ❌ | **✅** |
| **Codex support** | ❌ | ✅ | ❌ | ❌ |

## Install

**One-liner (recommended):**

```bash
curl -fsSL https://raw.githubusercontent.com/raketenkater/ultra-zen/main/install.sh | sh
```

**Go install:**

```bash
go install github.com/raketenkater/ultra-zen/cmd/ultra-zen@latest
```

**Build from source:**

```bash
git clone https://github.com/raketenkater/ultra-zen
cd ultra-zen
make build
```

### Prerequisites

- **Claude Code** installed and on `PATH` (`npm i -g @anthropic-ai/claude-code`).
- **opencode** logged in with the `opencode-go` provider (for `--provider opencode-go`):
  `opencode auth login` → select the opencode provider.
- **OpenRouter API key** (for `--provider openrouter`):
  Get one at [openrouter.ai/keys](https://openrouter.ai/keys).
- **uvx** (optional) for web research — `ultra-zen` wires a no-key DuckDuckGo
  MCP in place of the Anthropic-only `WebSearch` tool.

## Usage

```bash
ultra-zen                              # interactive TUI → opencode Zen
ultra-zen --provider openrouter        # OpenRouter free models
ultra-zen glm-5.1                      # skip the selector, use this model
ultra-zen kimi-k2.6 -- --resume        # extra args pass through to claude
ultra-zen --list                       # list available models and exit
ultra-zen --list --provider openrouter # list OpenRouter free models
ultra-zen --proxy-only glm-5.1         # start the proxy and block (debugging)
ultra-zen --port 8787 glm-5.1          # pin a fixed port
ultra-zen --version                    # print version and exit

# Orchestrator/worker split — use a smart model for planning, a cheap one for fan-out
ultra-zen glm-5.1 --worker mini-max-m2.5    # 3x more UltraCode agents per quota
ultra-zen kimi-k3 --worker deepseek-v4-flash-free  # Go orchestrator + free worker
```

### Orchestrator / worker split

`--worker <model>` gives you two models behind one proxy:

- **Orchestrator** (`<model>`): handles the main Claude Code loop — planning,
  tool-use decisions, synthesis. Gets a smart model.
- **Worker** (`--worker`): handles background sub-agents spawned by UltraCode
  workflows. Gets a cheap, high-rate-limit model.

The proxy classifies each request by inspecting the tool list: interactive
tools (`AskUserQuestion`, `Skill`, `EnterPlanMode`) mean orchestrator;
everything else uses the worker. This means you can fan out 20 parallel
sub-agents without burning your expensive orchestrator quota.

On local hardware this makes no difference (every token costs the same), so
omit `--worker` and everything uses one model.

### OpenRouter

```bash
# Get a key at https://openrouter.ai/keys
export OPENROUTER_API_KEY=sk-or-v1-your-key
ultra-zen --provider openrouter
```

Free OpenRouter models include `qwen/qwen3-coder:free`, `deepseek/deepseek-chat:free`,
`google/gemini-2.5-flash:free`, and the `openrouter/free` auto-router that
picks the best available free model automatically.

### opencode Go tier

The Go tier is **$5 first month, then $10/month** and provides generous
per-model request budgets (e.g. ~20,000 MiniMax M2.5 requests per 5 hours,
~31,000 DeepSeek V4 Flash). Subscribe at [opencode.ai/go](https://opencode.ai/go).

Flags must come **before** the model argument (standard Go flag parsing):
`ultra-zen --port 9000 --provider openrouter`.

## How it works

### Model selection
`ultra-zen` fetches the live model list:

- **opencode-go tier** (`https://opencode.ai/zen/go/v1`) — models your
  `opencode-go` key can access.
- **free tier** (`https://opencode.ai/zen/v1`) — the `*-free` models.
- **OpenRouter** (`https://openrouter.ai/api/v1`) — `:free` models and the
  `openrouter/free` auto-router.

### Translation proxy
The proxy (`internal/proxy`) translates both directions:

| Anthropic (Claude Code)        | OpenAI (Zen gateway)            |
|--------------------------------|---------------------------------|
| `system` (string or blocks)    | `messages[0]` role `system`     |
| `messages[].content` blocks    | `content` / `tool_calls` / `tool` |
| `tools[].input_schema`         | `tools[].function.parameters`   |
| `tool_choice`                  | `tool_choice` (`auto`/`required`/`function`) |
| `stop_reason`                  | `finish_reason`                 |
| SSE `message_start`…`message_stop` | SSE `data:` chunks            |

Reasoning models (e.g. `glm-5.1`) emit the answer in `reasoning_content`; the
proxy surfaces it as a text block so Claude Code is never fed an empty message.

The proxy also handles: auto-retry on upstream 400s, `max_tokens` clamping (Zen
gateway rejects outsized values), dangling tool call repair (after compaction),
and DeepSeek `reasoning_content` placeholders.

### Claude Code environment
The launcher sets these environment variables:
- `ANTHROPIC_BASE_URL` → the local proxy
- `ANTHROPIC_AUTH_TOKEN=ultra-zen`, `ANTHROPIC_MODEL` + all tier vars → selected model
- Watchdog/timeouts relaxed so a remote model never trips Claude Code's idle timers
- `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE=60` (compact early for smaller context windows)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="ultra-zen-launch.png">
  <img alt="ultra-zen launched with claude" src="ultra-zen-launch.png" width="660">
</picture>

### Full feature parity
- **UltraCode / Workflow**: a `PreToolUse` hook rewrites Workflow `agent()`
  scripts to set `stallMs` to the maximum safe value, so background fan-out
  never aborts a quiet model.
- **Web research**: the Anthropic-only `WebSearch` tool is disabled and replaced
  with a DuckDuckGo MCP (`mcp__ddg-search__search`, `mcp__ddg-search__fetch_content`)
  when `uvx` is present.
- **Subagents / background agents**: routed to the same selected model via the
  tier env vars.
- **Tools, MCP, hooks, streaming**: all pass through the proxy transparently.

## Project layout

```
cmd/ultra-zen/      entry point: selector + launcher + provider dispatch
internal/auth/      read opencode-go key from auth.json (or env for OpenRouter)
internal/models/    fetch live model list from Zen gateway or OpenRouter
internal/proxy/     Anthropic↔OpenAI translation (request/response/stream)
internal/claude/    env build + settings injection + exec
internal/tui/       Bubble Tea model selector
internal/workflow/  Workflow script rewriter (stallMs injection hook)
```

## Related tools

These projects also bridge Claude Code to non-Anthropic backends:

- [claude-code-proxy](https://github.com/1rgs/claude-code-proxy) (3.7k ★) —
  Python/LiteLLM proxy for Anthropic, OpenAI, and Gemini backends. Requires API keys.
- [UltraCode-Shim](https://github.com/OnlyTerp/UltraCode-Shim) (412 ★) —
  Python proxy with orchestrator/worker model splitting and auto-router. Requires API keys.
- [zen-proxy](https://github.com/azain47/zen-proxy) — Single Go binary proxy for
  opencode Zen + OpenRouter. Broader protocol support (Claude Code + Codex + OpenAI clients).

ultra-zen is unique in targeting **free models with zero configuration** — read
your existing opencode auth, pick a model, and go. No API key purchases, no
Python environments, no config files.

## License

MIT
