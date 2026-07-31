package main

import (
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/keys"
)

// validKeyProvider should accept every provider ultra-zen can store a key
// for and reject everything else.
func TestValidKeyProvider(t *testing.T) {
	valid := []string{"openrouter", "opencode-go", "groq", "cerebras", "huggingface", "cohere", "modelscope"}
	for _, p := range valid {
		if !validKeyProvider(p) {
			t.Errorf("validKeyProvider(%q) = false, want true", p)
		}
	}
	invalid := []string{"codex", "", "openai", "anthropic", "nonsense"}
	for _, p := range invalid {
		if validKeyProvider(p) {
			t.Errorf("validKeyProvider(%q) = true, want false", p)
		}
	}
}

// listKeys should print a status line per provider and never print a secret.
func TestListKeysNeverPrintsSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Store a key with a distinctive secret.
	if err := keys.Save("modelscope", "sk-super-secret-value"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Capture stdout.
	var buf strings.Builder
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	listKeys()

	out := buf.String()
	if !strings.Contains(out, "modelscope") {
		t.Errorf("listKeys output missing modelscope row: %q", out)
	}
	if strings.Contains(out, "sk-super-secret-value") {
		t.Errorf("listKeys leaked the secret: %q", out)
	}
}
