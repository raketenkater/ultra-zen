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
	"path/filepath"
	"strings"
	"time"

	"github.com/raketenkater/ultra-zen/internal/workflow"
)

// JavaScript's maximum safe timer value: ~24.8 days, effectively no deadline.
const noTimeoutMS = 2147483647

// contextWindowDefault is the context length assumed when the gateway's model
// metadata doesn't include one. 200k is Claude Code's default assumption for
// custom endpoints; it's generous for Zen models (which are typically 128k-262k)
// but safe — the autocompact percentage scales it down.
const contextWindowDefault = 200_000

// Env returns the child environment that points Claude Code at the local proxy.
// Every inference tier maps to the same model so background/subagent work stays
// on the selected Zen model. The real ANTHROPIC_API_KEY is dropped so the dummy
// auth token + base URL take effect.
//
// contextLength is the model's real context window in tokens as reported by the
// gateway's GET /models endpoint. It sets CLAUDE_MAX_SESSION_TOKENS so Claude
// Code knows the actual window and can compute the autocompact threshold
// correctly. When zero (gateway didn't report it), a safe default is used.
func Env(proxyURL, model string, contextLength int) []string {
	maxTokens := contextLength
	if maxTokens <= 0 {
		maxTokens = contextWindowDefault
	}
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
			"API_TIMEOUT_MS", "API_FORCE_IDLE_TIMEOUT", "CLAUDE_ASYNC_AGENT_STALL_TIMEOUT_MS",
			"CLAUDE_ENABLE_BYTE_WATCHDOG", "CLAUDE_ENABLE_STREAM_WATCHDOG",
			"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "CLAUDE_MAX_SESSION_TOKENS",
			"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
			"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL":
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
		// /model gateway discovery: Claude Code fetches /v1/models from the
		// proxy when CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY is set AND the
		// base URL is NOT treated as first-party (hf() must be false). The proxy
		// URL (127.0.0.1) already fails the api.anthropic.com host check, so no
		// first-party override is set — and _CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL
		// must stay unset, because it would make hf() true and DISABLE discovery.
		// The proxy advertises claude-prefixed ids so the discovery filter
		// (/(claude|anthropic)/i) keeps them.
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
		fmt.Sprintf("API_TIMEOUT_MS=%d", noTimeoutMS),
		fmt.Sprintf("CLAUDE_ASYNC_AGENT_STALL_TIMEOUT_MS=%d", noTimeoutMS),
		"CLAUDE_ENABLE_BYTE_WATCHDOG=0",
		"CLAUDE_ENABLE_STREAM_WATCHDOG=0",
		"API_FORCE_IDLE_TIMEOUT=0",
		// Set the real context window so Claude Code's autocompaction engine
		// knows exactly when to compact. Without this Claude Code defaults to a
		// 200k assumption, so the conversation overflows the gateway's real limit
		// and fails with "context_length" before compaction ever triggers.
		//
		// Claude Code 2.1.x reads CLAUDE_CODE_AUTO_COMPACT_WINDOW (a token count,
		// min'd against the model's window from /v1/models) for this, NOT
		// CLAUDE_MAX_SESSION_TOKENS (verified: that var is absent from the 2.1.233
		// binary). We set BOTH: the real one for the window, plus the legacy
		// MAX_SESSION_TOKENS for older builds that still read it. The percentage
		// override compacts close to the limit to maximise usable context while
		// leaving a little headroom for tool-call overhead that the tokeniser
		// doesn't count.
		//
		// CLAUDE_AUTOCOMPACT_PCT_OVERRIDE is intentionally NOT inherited from the
		// environment: the Claude Code process itself exports a stray 70 into its
		// child env (verified in this session), which would silently override the
		// 85 default below. The inherited value is stripped in the loop above, so
		// ultra-zen always applies its own default. Users who want a different
		// threshold can set ultra-zen's own config (e.g. CLAUDE_AUTOCOMPACT_PCT
		// through the settings file) rather than a raw env var Claude Code also
		// manages.
		fmt.Sprintf("CLAUDE_CODE_AUTO_COMPACT_WINDOW=%d", maxTokens),
		fmt.Sprintf("CLAUDE_MAX_SESSION_TOKENS=%d", maxTokens),
		fmt.Sprintf("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE=%d", defaultAutocompactPCT),
	)
}

// defaultAutocompactPCT is the fraction of the context window at which Claude
// Code compacts. 85 is a middle ground between Claude Code's default ~92% and
// the old 70% — enough headroom to avoid overflow but far more usable context
// than compacting at 70%. It is a constant (not inherited from the env) so a
// stray CLAUDE_AUTOCOMPACT_PCT_OVERRIDE exported by the host Claude process
// cannot silently override it.
const defaultAutocompactPCT = 85

// GatewayCachePath returns the Claude Code gateway-models cache file that feeds
// its /model command. The path is resolved exactly as the installed binary does:
// Rms.join(Ln(), "cache", "gateway-models.json") where Ln() is the CONFIG dir —
// CLAUDE_CONFIG_DIR if set, else ~/.claude — NOT the XDG cache dir. Verified by
// decompiling the Claude Code 2.1.226 binary: Ln()=pn(()=>(bcc()??
// gwe.join(vcc.homedir(),".claude")).normalize("NFC")), bcc()=CLAUDE_CONFIG_DIR,
// _pu()=Rms.join(ypu(),"gateway-models.json"). The old code consulted
// CLAUDE_CODE_GATEWAY_CACHE_DIR (which does not exist in the binary) and wrote to
// ~/.cache/claude/cache, a file /model never reads — leaving the picker stale.
func GatewayCachePath() string {
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(configDir, "cache", "gateway-models.json")
}

// legacyGatewayCachePath is the XDG-style path older Claude Code builds may
// still read (~/.cache/claude/cache/gateway-models.json). WriteGatewayCache also
// writes here as a belt-and-suspenders fallback so a catalog pre-write reaches
// every binary variant.
func legacyGatewayCachePath() string {
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cache = filepath.Join(home, ".cache")
	}
	return filepath.Join(cache, "claude", "cache", "gateway-models.json")
}

// GatewayCacheModel is one entry in the gateway-models cache file. It matches
// the schema Claude Code validates with (hpu = {id, display_name?}.strip()).
type GatewayCacheModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// WriteGatewayCache pre-writes Claude Code's /model gateway-models cache so the
// picker shows the full catalog immediately on launch — without depending on
// Claude Code's own (fragile) gateway discovery fetch (bpu) to populate it.
//
// Claude Code's cnn() reads this file and maps every entry into the /model
// picker when CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY is set and the file's
// baseUrl matches ANTHROPIC_BASE_URL. ultra-zen therefore writes exactly the
// shape the binary expects: {baseUrl, fetchedAt, models:[{id, display_name}]}.
//
// models are the advertised catalog entries (already claude-prefixed ids, since
// the /model discovery filter drops anything not matching /(claude|anthropic)/i).
// baseURL must be the proxy URL passed to claude as ANTHROPIC_BASE_URL, byte-for-
// byte, or cnn() ignores the cache.
func WriteGatewayCache(baseURL string, models []GatewayCacheModel) error {
	cache := map[string]any{
		"baseUrl":   baseURL,
		"fetchedAt": time.Now().UTC().Format(time.RFC3339),
		"models":    models,
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("encode gateway cache: %w", err)
	}
	// Primary: the path Claude Code's /model reader (cnn) actually reads.
	path := GatewayCachePath()
	if path == "" {
		return fmt.Errorf("gateway cache path unavailable")
	}
	if err := writeGatewayCacheFile(path, data); err != nil {
		return err
	}
	// Belt-and-suspenders: also write the legacy XDG path so older binaries that
	// may read it still see the catalog. A failure here is non-fatal.
	if legacy := legacyGatewayCachePath(); legacy != "" && legacy != path {
		_ = writeGatewayCacheFile(legacy, data)
	}
	return nil
}

// writeGatewayCacheFile atomically writes the cache: temp file then rename, so a
// concurrent Claude Code read never sees a partially-written cache.
func writeGatewayCacheFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("gateway cache dir: %w", err)
	}
	tmp := path + ".ultra-zen-tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("gateway cache write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("gateway cache replace: %w", err)
	}
	return nil
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
//
// The payload carries the ultracode flag and the effort level. --settings
// replaces the project .claude/settings.json for this session, so if ultracode
// and effortLevel are not emitted here, a project that opts in via its settings
// file (or sets an effortLevel there) would launch without either. The effort
// default is "max" (ultracode's full thinking budget); a user's explicit
// --effort flag on the CLI wins because Args() only injects --effort when the
// user didn't pass one.
func SettingsJSON(hookCmd string) string {
	settings := map[string]any{
		"ultracode":    true,
		"effortLevel":  defaultEffort,
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

// defaultEffort is the session-wide effort level ultra-zen sets as the general
// default (ultracode's full thinking budget). A user's explicit --effort flag
// on the CLI wins over this injected setting.
const defaultEffort = "max"

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
// Ultracode workflows and agents able to do online research.
//
// uvxPresent reports whether the uvx runner (which executes the no-key
// DuckDuckGo MCP server) is on PATH. It is a var so tests can stub it; the
// production value is exec.LookPath.
var uvxPresent = func() bool {
	_, err := exec.LookPath("uvx")
	return err == nil
}

// The three flags are treated as one coherent block: WebSearch is only
// disabled when a working replacement (the DDG MCP) is actually wired in.
// Without uvx there is no replacement, so disabling WebSearch would leave web
// research silently unavailable — in that case we leave WebSearch as-is and
// return nothing. User-supplied --disallowedTools, --mcp-config, or
// --allowedTools/--allowed-tools always win over these defaults.
func researchArgs(userArgs []string) []string {
	if hasArg(userArgs, "--mcp-config") {
		return nil
	}
	if !uvxPresent() {
		return nil
	}
	var out []string
	if !hasArg(userArgs, "--disallowedTools") {
		out = append(out, "--disallowedTools", "WebSearch")
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
