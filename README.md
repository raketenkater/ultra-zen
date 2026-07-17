# ultra-zen

Run **Claude Code** on **any opencode Zen model** — the full opencode-go tier
(glm, kimi, minimax, qwen, deepseek, grok, mimo, hy3-preview) plus the
`*-free` models — with **full Claude Code feature parity** (Ultracode workflows,
subagents, MCP, hooks, WebFetch, tools, streaming).

`ultra-zen` is a launch wrapper around Claude Code. It reads your opencode API
key, lists the models available on the Zen gateway, lets you pick one from a
searchable TUI, starts a local **Anthropic → OpenAI translation proxy**, and
execs `claude` pointed at it. The architecture is borrowed from
[ggrun](https://github.com/raketenkater/ggrun): Claude Code only speaks the
Anthropic Messages API, so a thin bridge translates to the OpenAI Chat
Completions format the Zen gateway serves.

```
ultra-zen
  ├─ read opencode-go key from ~/.local/share/opencode/auth.json
  ├─ fetch live model list (go tier + free tier)
  ├─ TUI selector  ──────────────  ported from ggrun's main list
  ├─ start proxy (goroutine) 127.0.0.1:8787
  │     Anthropic /v1/messages  →  OpenAI /chat/completions
  │     tools · streaming SSE · reasoning_content handling
  ├─ build claude env (ggrun's claudeCodeEnv recipe)
  ├─ inject --settings (Workflow stallMs hook) + DDG MCP for web research
  └─ exec claude "$@"   (proxy torn down on exit)
```

## Why a proxy is needed

ggrun points Claude Code straight at `llm-server` because llama.cpp serves the
**Anthropic API natively**. The opencode Zen gateway only serves **OpenAI Chat
Completions**, so `ultra-zen` runs an in-process translation proxy. Unlike
ggrun (which manages a separate `llm-server` process), the proxy is a goroutine
inside the same binary — it starts and stops with the launcher automatically,
leaving no orphan processes.

## Install

```bash
go install github.com/raketenkater/ultra-zen/cmd/ultra-zen@latest
```

Or build from source:

```bash
git clone https://github.com/raketenkater/ultra-zen
cd ultra-zen
go build -o ultra-zen ./cmd/ultra-zen
```

### Prerequisites

- **Claude Code** installed and on `PATH` (`npm i -g @anthropic-ai/claude-code`).
- **opencode** logged in with the `opencode-go` provider:
  `opencode auth login` → select the opencode provider. The API key is read
  from `~/.local/share/opencode/auth.json` (or `~/.config/opencode/auth.json`).
- **uvx** (optional) for web research — `ultra-zen` wires a no-key DuckDuckGo
  MCP in place of the Anthropic-only `WebSearch` tool, exactly like ggrun.

## Usage

```bash
ultra-zen                 # interactive TUI model selector, then launch claude
ultra-zen glm-5.1         # skip the selector, use this model
ultra-zen kimi-k2.6 -- --resume   # extra args pass through to claude
ultra-zen --list          # list available models and exit
ultra-zen --proxy-only glm-5.1    # start the proxy and block (debugging)
```

Flags must come **before** the model argument (standard Go flag parsing):
`ultra-zen --port 9000 glm-5.1`.

## How it works

### Model selection
`ultra-zen` fetches the live model list from the Zen gateway:
- **opencode-go tier** (`https://opencode.ai/zen/go/v1`) — all 22 models your
  `opencode-go` key can use.
- **free tier** (`https://opencode.ai/zen/v1`) — the `*-free` models.

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

### Claude Code environment
The launcher sets (ported from ggrun's `claudeCodeEnv`):
- `ANTHROPIC_BASE_URL` → the local proxy
- `ANTHROPIC_AUTH_TOKEN=ultra-zen`, `ANTHROPIC_MODEL` + all tier vars → selected model
- watchdog/timeouts relaxed so a remote model never trips Claude Code's idle timers
- `DISABLE_PROMPT_CACHING=1`

### Full feature parity
- **Ultracode / Workflow**: a `PreToolUse` hook rewrites Workflow `agent()`
  scripts to set `stallMs` to the maximum safe value (ggrun's technique), so
  background fan-out never aborts a quiet model.
- **Web research**: the Anthropic-only `WebSearch` tool is disabled and replaced
  with a DuckDuckGo MCP (`mcp__ddg-search__search`, `mcp__ddg-search__fetch_content`)
  when `uvx` is present.
- **Subagents / background agents**: routed to the same selected model via the
  tier env vars.
- **Tools, MCP, hooks, streaming**: all pass through the proxy transparently.

## Project layout

```
cmd/ultra-zen/      entry point: selector + launcher
internal/auth/      read opencode-go key from auth.json
internal/models/    fetch live model list from the Zen gateway
internal/proxy/     Anthropic↔OpenAI translation (request/response/stream)
internal/claude/    env build + settings injection + exec
internal/tui/       Bubble Tea model selector (ported from ggrun)
```

## License

MIT
