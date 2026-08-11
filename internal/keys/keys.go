// Package keys is a tiny persistent store for provider API keys. Each key is
// kept as a plain text file with the provider name as the filename, so a shell
// glob can source every key as an env var and the store stays greppable.
//
// Two stores exist:
//
//   - The per-user store under ~/.config/ultra-zen/keys (0600 files, 0700 dir),
//     the default. This is where interactive key prompts land.
//   - The system store under /etc/ultra-zen/keys (0644 files, 0711 dir,
//     root-owned), which shares credentials with every local user so any user
//     on the machine can launch ultra-zen. Writing it requires root.
//
// Load resolves user-first, system-second: a per-user key always wins over the
// shared system key, so any user can opt out of the shared credential for
// their own sessions. Reading and writing is best-effort: a missing/corrupt
// entry just yields no key rather than blocking a launch, matching the other
// ultra-zen stores (see internal/models/recent.go).
package keys

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store identifies which key store an operation targets.
type Store int

const (
	// StoreUser is the per-user store (~/.config/ultra-zen/keys).
	StoreUser Store = iota
	// StoreSystem is the machine-wide store (/etc/ultra-zen/keys).
	StoreSystem
)

// systemDirOverride is set at runtime only in tests (ULTRA_ZEN_SYSTEM_KEYS).
// It lets tests redirect the system store away from /etc/ultra-zen.
func systemDirOverride() string { return os.Getenv("ULTRA_ZEN_SYSTEM_KEYS") }

// SystemDir returns the system-wide keys directory. It honours
// ULTRA_ZEN_SYSTEM_KEYS (a test/dev override) and defaults to
// /etc/ultra-zen/keys.
func SystemDir() string {
	if d := systemDirOverride(); d != "" {
		return d
	}
	return "/etc/ultra-zen/keys"
}

// dir returns the per-user keys directory, honouring XDG_CONFIG_HOME and
// defaulting to ~/.config/ultra-zen/keys.
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

// Path returns the per-user store directory so callers can tell the user
// where keys live.
func Path() string { return dir() }

// PathFor returns the directory for the given store.
func PathFor(s Store) string {
	if s == StoreSystem {
		return SystemDir()
	}
	return Path()
}

// providerPath resolves a provider entry within a store directory without
// allowing callers to escape it. Provider names are intentionally simple
// filenames.
func providerPath(dir, provider string) (string, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" || provider == "." || provider == ".." || filepath.Base(provider) != provider {
		return "", fmt.Errorf("invalid provider name %q", provider)
	}
	if dir == "" {
		return "", fmt.Errorf("cannot resolve config directory")
	}
	return filepath.Join(dir, provider), nil
}

// storeDir resolves the base directory for a store, or "" if it cannot be
// resolved (missing home dir for the user store).
func storeDir(s Store) string {
	switch s {
	case StoreSystem:
		return SystemDir()
	default:
		return dir()
	}
}

// fileMode is the permission for a stored key file: 0600 for the per-user
// store, 0644 for the system store (world-readable so every local user can
// use the shared credential — the deliberate trade-off of system-wide access).
func fileMode(s Store) os.FileMode {
	if s == StoreSystem {
		return 0o644
	}
	return 0o600
}

// Load returns the stored key for provider, or "". A per-user key wins; the
// system store is the fallback so the machine-wide shared credential covers
// users with no personal key.
func Load(provider string) string {
	if k := LoadFrom(provider, StoreUser); k != "" {
		return k
	}
	return LoadFrom(provider, StoreSystem)
}

// LoadFrom returns the stored key for provider from one specific store, or "".
func LoadFrom(provider string, s Store) string {
	p, err := providerPath(storeDir(s), provider)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Save writes (or overwrites) the per-user key for provider. An empty value
// removes the stored key. Errors are swallowed: failing to remember a key
// must not stop a launch.
func Save(provider, key string) error {
	return save(provider, key, StoreUser)
}

// SaveSystem writes (or overwrites) the system-wide key for provider. An empty
// value removes it. Requires write access to the system store (root); a
// non-root caller gets an error so the setup/keys flow can show a sudo hint
// instead of silently failing or falling back to the user store.
func SaveSystem(provider, key string) error {
	return save(provider, key, StoreSystem)
}

// save writes one key to one store with the store's file mode. An empty value
// removes the entry. The tmp+rename pattern keeps a concurrent reader from
// ever seeing a partially-written key.
func save(provider, key string, s Store) error {
	d := storeDir(s)
	if d == "" {
		return fmt.Errorf("cannot resolve config directory")
	}
	p, err := providerPath(d, provider)
	if err != nil {
		return err
	}
	mode := fileMode(s)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// Write to a temp file in the same dir, then rename over the target, so a
	// concurrent Load never reads a half-written key.
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	// OpenFile does not narrow permissions on an existing temp file; enforce
	// the store's mode explicitly (matters when the tmp already exists).
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.WriteString(key); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Has reports whether a key is stored for provider (user or system).
func Has(provider string) bool { return Load(provider) != "" }

// HasIn reports whether a key is stored for provider in one specific store.
func HasIn(provider string, s Store) bool { return LoadFrom(provider, s) != "" }
