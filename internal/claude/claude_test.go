package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestWriteGatewayCache verifies the /model gateway cache is written in the
// exact shape Claude Code's cnn() expects (baseUrl, fetchedAt, models with
// claude-prefixed ids), to the CONFIG dir path the binary actually reads
// (CLAUDE_CONFIG_DIR || ~/.claude), plus the legacy XDG path.
func TestWriteGatewayCache(t *testing.T) {
	configHome := t.TempDir()
	xdgHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("XDG_CACHE_HOME", xdgHome)

	err := WriteGatewayCache("http://127.0.0.1:1234", []GatewayCacheModel{
		{ID: "claude-opencode-go-deepseek-v4-flash", DisplayName: "deepseek-v4-flash"},
		{ID: "claude-codex-gpt-5.6-sol", DisplayName: "GPT-5.6-Sol"},
	})
	if err != nil {
		t.Fatalf("WriteGatewayCache: %v", err)
	}
	// Primary path: what cnn() reads.
	path := filepath.Join(configHome, "cache", "gateway-models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cache not written at %s: %v", path, err)
	}
	var cache map[string]any
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatalf("cache not valid JSON: %v", err)
	}
	if cache["baseUrl"] != "http://127.0.0.1:1234" {
		t.Fatalf("baseUrl = %v", cache["baseUrl"])
	}
	models, _ := cache["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models = %d entries, want 2", len(models))
	}
	first := models[0].(map[string]any)
	if first["id"] != "claude-opencode-go-deepseek-v4-flash" || first["display_name"] != "deepseek-v4-flash" {
		t.Fatalf("first model = %v", first)
	}
	// Legacy XDG path: also written as a belt-and-suspenders fallback.
	legacyPath := filepath.Join(xdgHome, "claude", "cache", "gateway-models.json")
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy cache not written at %s: %v", legacyPath, err)
	}
}

// TestGatewayCachePathPrecedence verifies GatewayCachePath resolves CLAUDE_CONFIG_DIR
// over the ~/.claude fallback, exactly as the binary's Ln() does.
func TestGatewayCachePathPrecedence(t *testing.T) {
	config := filepath.Join(t.TempDir(), "custom-config")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	if got := GatewayCachePath(); got != filepath.Join(config, "cache", "gateway-models.json") {
		t.Fatalf("GatewayCachePath = %q, want config-dir path %q", got, filepath.Join(config, "cache", "gateway-models.json"))
	}
	// ~/.claude fallback when CLAUDE_CONFIG_DIR is unset. UserHomeDir() reads the
	// OS home (HOME env on Linux); set it so the fallback is deterministic.
	home := t.TempDir()
	origConfig := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", home)
	t.Cleanup(func() {
		os.Setenv("CLAUDE_CONFIG_DIR", origConfig)
		os.Setenv("HOME", origHome)
	})
	if got := GatewayCachePath(); got != filepath.Join(home, ".claude", "cache", "gateway-models.json") {
		t.Fatalf("GatewayCachePath with HOME=%s = %q, want ~/.claude path", home, got)
	}
}

// TestEnvGatewayDiscovery ensures Claude Code's /model gateway discovery is
// enabled and the assume-first-party override stays unset (setting it would
// make hf() true and DISABLE discovery, since gpu() bails when hf() is true).
func TestEnvGatewayDiscovery(t *testing.T) {
	// Run with a clean slate for the keys Env() overrides.
	orig := map[string]string{}
	for _, k := range []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
		"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL", "ANTHROPIC_API_KEY",
	} {
		orig[k] = os.Getenv(k)
		defer os.Setenv(k, orig[k])
		os.Unsetenv(k)
	}

	env := Env("http://127.0.0.1:1234", "deepseek-v4-flash", 200000)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
		t.Fatalf("gateway discovery not enabled: %q", got["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"])
	}
	// The assume-first-party override must NOT be set: it would make hf() true,
	// which gpu() treats as "real Anthropic, no discovery needed" and returns
	// false, hiding the catalog.
	if _, ok := got["_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL"]; ok {
		t.Fatalf("_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL must stay unset; it disables discovery")
	}
	if got["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:1234" {
		t.Fatalf("base URL = %q", got["ANTHROPIC_BASE_URL"])
	}
	if _, leaked := got["ANTHROPIC_API_KEY"]; leaked {
		t.Fatalf("real API key leaked into env")
	}
}

// TestSettingsJSONCarriesEffortDefault verifies the injected --settings payload
// carries the ultracode flag AND the effortLevel general default, so a fresh
// ultra-zen session always starts at full ultracode thinking budget even when
// --settings replaces the project's settings.json.
func TestSettingsJSONCarriesEffortDefault(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(SettingsJSON("bin workflow-hook")), &payload); err != nil {
		t.Fatalf("SettingsJSON not valid JSON: %v", err)
	}
	if payload["ultracode"] != true {
		t.Fatalf("ultracode = %v, want true", payload["ultracode"])
	}
	if got := payload["effortLevel"]; got != "max" {
		t.Fatalf("effortLevel = %v, want max (ultracode default)", got)
	}
	// The Workflow stallMs hook must still be present.
	hooks, _ := payload["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("hooks missing from settings")
	}
}

// TestArgsUserEffortWins verifies an explicit user --effort is preserved and
// the injected default is skipped, so the user's choice is never clobbered.
func TestArgsUserEffortWins(t *testing.T) {
	args := Args("deepseek-v4-flash", "bin workflow-hook", []string{"--effort", "medium"})
	got := strings.Join(args, " ")
	if !strings.Contains(got, "--effort medium") {
		t.Fatalf("user --effort lost: %q", got)
	}
	if strings.Count(got, "--effort") != 1 {
		t.Fatalf("expected exactly one --effort (user's); got %q", got)
	}
}

func TestResearchArgs(t *testing.T) {
	const mcpCfg = `{"mcpServers":{"ddg-search":{"command":"uvx","args":["duckduckgo-mcp-server"]}}}`
	const allowed = "mcp__ddg-search__search,mcp__ddg-search__fetch_content"

	// Stub uvx availability so the test is independent of the machine.
	orig := uvxPresent
	t.Cleanup(func() { uvxPresent = orig })

	fullBlock := []string{"--disallowedTools", "WebSearch", "--mcp-config", mcpCfg, "--allowedTools", allowed}

	tests := []struct {
		name string
		user []string
		uvx  bool
		want []string
	}{
		{
			name: "uvx present: emits coherent WebSearch-disable + DDG MCP block",
			uvx:  true,
			want: fullBlock,
		},
		{
			name: "no uvx: leaves WebSearch untouched (no dead end)",
			uvx:  false,
			want: nil,
		},
		{
			name: "user --mcp-config wins: no WebSearch disable, no MCP wiring",
			user: []string{"--mcp-config", `{"mcpServers":{"x":{}}}`},
			uvx:  true,
			want: nil,
		},
		{
			name: "user --disallowedTools wins: DDG MCP wired, no WebSearch disable",
			user: []string{"--disallowedTools", "Custom"},
			uvx:  true,
			want: []string{"--mcp-config", mcpCfg, "--allowedTools", allowed},
		},
		{
			name: "user --allowed-tools wins: disable + MCP, no allowlist",
			user: []string{"--allowed-tools", "SomeTool"},
			uvx:  true,
			want: []string{"--disallowedTools", "WebSearch", "--mcp-config", mcpCfg},
		},
		{
			name: "user --allowedTools (camel) wins: disable + MCP, no allowlist",
			user: []string{"--allowedTools", "SomeTool"},
			uvx:  true,
			want: []string{"--disallowedTools", "WebSearch", "--mcp-config", mcpCfg},
		},
		{
			name: "user passes own disallowed + allowed: only MCP config wired",
			user: []string{"--disallowedTools", "Custom", "--allowed-tools", "Y"},
			uvx:  true,
			want: []string{"--mcp-config", mcpCfg},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uvxPresent = func() bool { return tc.uvx }
			got := researchArgs(tc.user)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("researchArgs(%v) with uvx=%v = %v, want %v", tc.user, tc.uvx, got, tc.want)
			}
		})
	}
}
