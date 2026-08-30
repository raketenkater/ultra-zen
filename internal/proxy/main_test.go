package proxy

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points the OpenRouter :free counter at a throwaway cache dir so
// tests that serve 200s through openrouter :free routes (proxy_test.go,
// retry_test.go) never bump the developer's real daily tally. The counter is
// keyed by XDG_CACHE_HOME (internal/models/openrouterquota.go); the pace
// files use XDG_RUNTIME_DIR/HOME and are unaffected.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ultra-zen-proxy-test-cache-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "proxy TestMain: "+err.Error())
		os.Exit(1)
	}
	os.Setenv("XDG_CACHE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
