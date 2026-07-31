package models

import (
	"testing"

	"github.com/raketenkater/ultra-zen/internal/keys"
)

func TestProviderKeyUsesSavedOpenCodeKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := keys.Save("opencode-go", "saved-zen-key"); err != nil {
		t.Fatal(err)
	}
	if got := ProviderKey("opencode-go", "", ""); got != "saved-zen-key" {
		t.Fatalf("ProviderKey = %q, want saved opencode key", got)
	}
	if got := ProviderKey("opencode-go", "", "auth-key"); got != "auth-key" {
		t.Fatalf("explicit Zen key = %q, want auth-key", got)
	}
}
