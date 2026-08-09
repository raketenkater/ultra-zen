package claude

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestEnvGatewayDiscovery ensures Claude Code's /model gateway discovery is
// enabled and the proxy URL is treated as first-party, so the /v1/models
// catalog the proxy advertises is actually fetched.
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
	if got["_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL"] != "1" {
		t.Fatalf("assume-first-party not set: %q", got["_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL"])
	}
	if got["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:1234" {
		t.Fatalf("base URL = %q", got["ANTHROPIC_BASE_URL"])
	}
	if _, leaked := got["ANTHROPIC_API_KEY"]; leaked {
		t.Fatalf("real API key leaked into env")
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
