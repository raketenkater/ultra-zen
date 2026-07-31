// Package keys is a tiny persistent store for provider API keys. Each key is
// kept as a plain text file under ~/.config/ultra-zen/keys/<provider> with
// 0600 permissions, so a shell glob can source every key as an env var
// (e.g. `export $(ls ~/.config/ultra-zen/keys | ...)`) and the store stays
// greppable/auditable. Reading and writing is best-effort: a missing/corrupt
// entry just yields no key rather than blocking a launch, matching the other
// ultra-zen stores (see internal/models/recent.go).
package keys

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dir returns the keys directory, honouring XDG_CONFIG_HOME and defaulting to
// ~/.config/ultra-zen/keys.
func dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ultra-zen", "keys")
}

// Path returns the store directory so callers can tell the user where keys
// live.
func Path() string { return dir() }

// providerPath resolves a provider entry without allowing callers to escape
// the key directory. Provider names are intentionally simple filenames.
func providerPath(provider string) (string, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" || provider == "." || provider == ".." || filepath.Base(provider) != provider {
		return "", fmt.Errorf("invalid provider name %q", provider)
	}
	d := dir()
	if d == "" {
		return "", fmt.Errorf("cannot resolve config directory")
	}
	return filepath.Join(d, provider), nil
}

// Load returns the stored key for provider, or "".
func Load(provider string) string {
	p, err := providerPath(provider)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Save writes (or overwrites) the key for provider with 0600 permissions so
// the secret is only readable by the owning user. An empty value removes the
// stored key. Errors are swallowed: failing to remember a key must not stop a
// launch.
func Save(provider, key string) error {
	d := dir()
	if d == "" {
		return fmt.Errorf("cannot resolve config directory")
	}
	p, err := providerPath(provider)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	// OpenFile does not narrow permissions on an existing file.
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(key); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Has reports whether a key is stored for provider.
func Has(provider string) bool { return Load(provider) != "" }
