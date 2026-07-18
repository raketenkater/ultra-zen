// Package claude builds the environment and arguments for exec'ing Claude Code
// pointed at the local ultra-zen proxy. It drops the real Anthropic API key,
// sets a dummy auth token and local base URL, maps every inference tier to the
// selected model, and relaxes the watchdogs so a remote-but-slower gateway never
// trips Claude Code's idle timers.
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/raketenkater/ultra-zen/internal/workflow"
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
		// Behind a custom base URL Claude Code assumes a 200k window and won't
		// auto-compact until ~92% (~184k tokens). The Zen gateway models have a
		// smaller real context, so the conversation overflows and the gateway
		// fails the request ("Upstream request failed" / context_length). Compact
		// early at a conservative percentage so agents never overflow. A user-set
		// CLAUDE_AUTOCOMPACT_PCT_OVERRIDE in the environment still takes precedence.
		"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE="+envOr("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "60"),
	)
}

// envOr returns the current environment value for key, or def if unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// SettingsJSON returns the inline --settings JSON injected at launch. It wires
// a PreToolUse hook that rewrites Workflow tool scripts to set stallMs to the
// maximum safe value, so Ultracode/Workflow fan-out never aborts a quiet model.
// The hook calls `ultra-zen workflow-hook`, which uses a proper JS tokenizer
// (internal/workflow) that understands strings, template literals and comments
// — unlike a naive regex, it cannot corrupt a script that has a ')' inside an
// agent prompt string literal. hookCmd is the absolute path to the ultra-zen
// binary so the hook works even if ultra-zen is not on PATH.

// SettingsJSON returns the settings payload passed via --settings. hookCmd is
// the command to run for the Workflow PreToolUse hook (typically the absolute
// path to the ultra-zen binary plus "workflow-hook").
func SettingsJSON(hookCmd string) string {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Workflow",
					"hooks": []map[string]any{
						{"type": "command", "command": hookCmd},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(settings)
	return string(b)
}

// Args returns the claude arguments: the selected model, the inline --settings,
// and any user-supplied passthrough args. The explicit --model makes ultra-zen
// self-contained: it overrides any "model" default in the user's global
// settings.json (e.g. "sonnet") so the session always uses the selected Zen
// model regardless of the user's existing Claude Code config. If the user
// already passed --model/-m we leave theirs alone. If the user already passed
// --settings, we merge by skipping our own to avoid clobbering theirs.
//
// hookCmd is the Workflow PreToolUse hook command (see SettingsJSON). A
// --append-system-prompt is also injected as belt-and-suspenders: if a Claude
// Code version or custom --settings suppresses hooks, the model still learns
// to set stallMs itself.
func Args(model, hookCmd string, userArgs []string) []string {
	var out []string
	if !hasArg(userArgs, "--model") && !hasArg(userArgs, "-m") {
		out = append(out, "--model", model)
	}
	if !hasArg(userArgs, "--settings") {
		out = append(out, "--settings", SettingsJSON(hookCmd))
	}
	// Default to the highest effort ("max") so a fresh ultra-zen session starts
	// at full thinking budget — matching the "ultracode effort" launch intent.
	// If the user already passed --effort, leave theirs alone.
	if !hasArg(userArgs, "--effort") {
		out = append(out, "--effort", "max")
	}
	out = append(out, workflowPromptArgs(userArgs)...)
	out = append(out, researchArgs(userArgs)...)
	out = append(out, userArgs...)
	return out
}

// workflowPromptArgs appends the Workflow stallMs instruction to the session
// system prompt. If the user already passed --append-system-prompt we merge
// into theirs instead of adding a second one.
func workflowPromptArgs(userArgs []string) []string {
	out := append([]string(nil), userArgs...)
	for i, arg := range out {
		if arg == "--append-system-prompt" && i+1 < len(out) {
			out[i+1] += "\n\n" + workflow.SystemPrompt
			return []string{"--append-system-prompt", out[i+1]}
		}
		if strings.HasPrefix(arg, "--append-system-prompt=") {
			return []string{"--append-system-prompt=" + arg + "\n\n" + workflow.SystemPrompt}
		}
	}
	return []string{"--append-system-prompt", workflow.SystemPrompt}
}

// researchArgs configures web research for a non-Anthropic endpoint: the
// built-in WebSearch/WebFetch tools are Anthropic server-side tools that
// cannot run against the local proxy, so we disable WebSearch and wire a
// no-key DuckDuckGo MCP in its place (when uvx is available). This keeps
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
