// Package auth locates the opencode API credential that ultra-zen reuses to
// talk to the Zen gateway. We deliberately do not store or accept the key
// ourselves; we read it from opencode's existing auth store so a single
// `opencode auth login` is the only setup step.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store is the subset of opencode's auth.json we care about.
type Store map[string]Entry

// Entry is one provider credential. Only the "api" type is useful to us.
type Entry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// DefaultPath returns opencode's canonical auth.json location. opencode stores
// credentials under the XDG data dir (~/.local/share/opencode on Linux), not the
// config dir, so we check the data dir first and fall back to the config dir.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate home dir: %w", err)
	}
	candidates := []string{
		filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		filepath.Join(home, ".config", "opencode", "auth.json"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Return the most likely default so the error message is helpful.
	return filepath.Join(home, ".local", "share", "opencode", "auth.json"), nil
}

// Load reads the opencode auth store from the given path (or the default).
func Load(path string) (Store, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read opencode auth (%s): %w\nRun `opencode auth login` first", path, err)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse opencode auth (%s): %w", path, err)
	}
	return s, nil
}

// KeyFor returns the API key for the named opencode provider (e.g. "opencode-go").
func KeyFor(s Store, provider string) (string, error) {
	e, ok := s[provider]
	if !ok {
		return "", fmt.Errorf("no %q entry in opencode auth.json; run `opencode auth login` and select the opencode provider", provider)
	}
	if e.Type != "api" || e.Key == "" {
		return "", fmt.Errorf("opencode auth entry %q is not an API-key credential (type=%q); re-login with `opencode auth login`", provider, e.Type)
	}
	return e.Key, nil
}

// KeyFromEnv reads an API key from an environment variable. Used for providers
// like OpenRouter that expect their key in the environment rather than in
// opencode's auth store.
func KeyFromEnv(envVar string) (string, error) {
	key := os.Getenv(envVar)
	if key == "" {
		return "", fmt.Errorf("environment variable %s is not set; export it or pass --auth", envVar)
	}
	return key, nil
}