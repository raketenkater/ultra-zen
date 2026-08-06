package claude

import (
	"reflect"
	"testing"
)

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
