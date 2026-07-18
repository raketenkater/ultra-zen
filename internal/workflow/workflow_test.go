package workflow

import (
	"strings"
	"testing"
)

func TestNoTimeoutScriptAddsStallMsToBareAgent(t *testing.T) {
	in := `const r = await agent('summarize the readme')`
	out := NoTimeoutScript(in)
	if !strings.Contains(out, "stallMs: 2147483647") {
		t.Fatalf("stallMs not added: %s", out)
	}
}

func TestNoTimeoutScriptPreservesAgentWithExistingStallMs(t *testing.T) {
	in := `await agent('do thing', { stallMs: 1000 })`
	out := NoTimeoutScript(in)
	// Should wrap with Object.assign so the new stallMs wins, not duplicate.
	if !strings.Contains(out, "Object.assign") {
		t.Fatalf("expected Object.assign wrap, got: %s", out)
	}
	if strings.Count(out, "stallMs:") != 2 {
		t.Fatalf("expected 2 stallMs (original + override), got: %s", out)
	}
}

// TestNoTimeoutScriptDoesNotCorruptParenInString is the regression that broke
// ultra-zen's old regex hook: a ')' inside an agent prompt string literal must
// not terminate the call scan early and mangle the script into invalid JS.
func TestNoTimeoutScriptDoesNotCorruptParenInString(t *testing.T) {
	in := `await agent('Search the web for: "Can I use Claude Code with a proxy", "competitors"')`
	out := NoTimeoutScript(in)
	// Must still be valid: the agent call gets a stallMs options arg appended.
	if !strings.Contains(out, "stallMs: 2147483647") {
		t.Fatalf("stallMs not added: %s", out)
	}
	// The original string literal must survive intact (not split by a premature ')').
	if !strings.Contains(out, `"Can I use Claude Code with a proxy"`) {
		t.Fatalf("string literal corrupted: %s", out)
	}
	if strings.Count(out, "agent(") != 1 {
		t.Fatalf("agent call count wrong: %s", out)
	}
}

func TestNoTimeoutScriptHandlesTemplateLiterals(t *testing.T) {
	in := "await agent(`prompt with ${value} and a ) paren`)"
	out := NoTimeoutScript(in)
	if !strings.Contains(out, "stallMs: 2147483647") {
		t.Fatalf("stallMs not added: %s", out)
	}
	if !strings.Contains(out, "${value}") {
		t.Fatalf("template interpolation corrupted: %s", out)
	}
}

func TestNoTimeoutScriptIgnoresMethodCall(t *testing.T) {
	// obj.agent(...) is a method call, not the Workflow agent() — must be left alone.
	in := `const r = obj.agent('foo')`
	out := NoTimeoutScript(in)
	if strings.Contains(out, "stallMs") {
		t.Fatalf("method call should not be rewritten: %s", out)
	}
}

func TestNoTimeoutScriptMultipleAgents(t *testing.T) {
	in := `const [a, b] = await parallel([
  () => agent('task one'),
  () => agent('task two (with paren)'),
])`
	out := NoTimeoutScript(in)
	if strings.Count(out, "stallMs: 2147483647") != 2 {
		t.Fatalf("expected 2 stallMs, got: %s", out)
	}
}