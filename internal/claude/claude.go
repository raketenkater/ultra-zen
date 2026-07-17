// Package claude builds the environment and arguments for exec'ing Claude Code
// pointed at the local ultra-zen proxy. The recipe is adapted from ggrun
// (llm-server cmd/ggrun/main.go claudeCodeEnv) and from the user's local_claude
// launcher: drop the real Anthropic key, set a dummy auth token + base URL, map
// every inference tier to the selected model, and relax the watchdogs so a
// remote-but-slower gateway never trips Claude Code's idle timers.
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// JavaScript's maximum safe timer value: ~24.8 days, effectively no deadline.
const noTimeoutMS = 2147483647

// Env returns the child environment that points Claude Code at the local proxy.
// Every inference tier maps to the same model so background/subagent work stays
// on the selected Zen model. The real ANTHROPIC_API_KEY is dropped so the dummy
// auth token + base URL take effect.
func Env(proxyURL, model string) []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
			"API_TIMEOUT_MS", "API_FORCE_IDLE_TIMEOUT", "CLAUDE_ASYNC_AGENT_STALL_TIMEOUT_MS",
			"CLAUDE_ENABLE_BYTE_WATCHDOG", "CLAUDE_ENABLE_STREAM_WATCHDOG",
			"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"ANTHROPIC_BASE_URL="+proxyURL,
		"ANTHROPIC_AUTH_TOKEN=ultra-zen",
		"ANTHROPIC_MODEL="+model,
		"ANTHROPIC_SMALL_FAST_MODEL="+model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL="+model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL="+model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL="+model,
		fmt.Sprintf("API_TIMEOUT_MS=%d", noTimeoutMS),
		fmt.Sprintf("CLAUDE_ASYNC_AGENT_STALL_TIMEOUT_MS=%d", noTimeoutMS),
		"CLAUDE_ENABLE_BYTE_WATCHDOG=0",
		"CLAUDE_ENABLE_STREAM_WATCHDOG=0",
		"API_FORCE_IDLE_TIMEOUT=0",
		"DISABLE_PROMPT_CACHING=1",
	)
}

// SettingsJSON returns the inline --settings JSON injected at launch. It wires
// a PreToolUse hook that rewrites Workflow tool scripts to set stallMs to the
// maximum safe value, so Ultracode/Workflow fan-out never aborts a quiet model.
// This mirrors ggrun's claude_workflow.go hook, but is self-contained: the hook
// command rewrites the script in-process without an external binary.
//
// hookScript is the shell command Claude Code runs for every Workflow tool
// invocation. It reads the hook JSON on stdin, rewrites agent() stallMs, and
// emits the updated input.
const hookScript = `node -e '
const fs=require("fs");
let s=fs.readFileSync(0,"utf8");
let j=JSON.parse(s);
if(j.tool_name==="Workflow"&&j.tool_input&&j.tool_input.script){
  j.tool_input.script=j.tool_input.script.replace(/agent\s*\(/g,"agent(").replace(/agent\(([^,)]*)\)/g,(m,a)=>"agent("+a+", { stallMs: 2147483647 })");
}
process.stdout.write(JSON.stringify({hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"allow",permissionDecisionReason:"ultra-zen Workflow policy",updatedInput:j.tool_input}}));
'`

// SettingsJSON returns the settings payload passed via --settings.
func SettingsJSON() string {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Workflow",
					"hooks": []map[string]any{
						{"type": "command", "command": hookScript},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(settings)
	return string(b)
}

// Args returns the claude arguments: the inline --settings plus any
// user-supplied passthrough args. If the user already passed --settings, we
// merge by skipping our own to avoid clobbering theirs.
func Args(userArgs []string) []string {
	out := []string{"--settings", SettingsJSON()}
	out = append(out, researchArgs(userArgs)...)
	out = append(out, userArgs...)
	return out
}

// researchArgs mirrors ggrun's approach to web research on a non-Anthropic
// endpoint: the built-in WebSearch/WebFetch tools are Anthropic server-side
// tools that cannot run against the local proxy, so we disable WebSearch and
// wire a no-key DuckDuckGo MCP in its place (when uvx is available). This keeps
// Ultracode workflows and agents able to do online research. If the user
// supplies their own --mcp-config or --disallowedTools we leave theirs alone.
func researchArgs(userArgs []string) []string {
	var out []string
	if !hasArg(userArgs, "--disallowedTools") {
		out = append(out, "--disallowedTools", "WebSearch")
	}
	if hasArg(userArgs, "--mcp-config") {
		return out
	}
	if _, err := exec.LookPath("uvx"); err != nil {
		return out
	}
	cfg := `{"mcpServers":{"ddg-search":{"command":"uvx","args":["duckduckgo-mcp-server"]}}}`
	out = append(out, "--mcp-config", cfg)
	if !hasArg(userArgs, "--allowedTools") && !hasArg(userArgs, "--allowed-tools") {
		out = append(out, "--allowedTools", "mcp__ddg-search__search,mcp__ddg-search__fetch_content")
	}
	return out
}

// hasArg reports whether args contains the given flag (as a bare flag or
// --flag=value).
func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}