package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raketenkater/ultra-zen/internal/flock"
	"github.com/raketenkater/ultra-zen/internal/models"
)

func freePoolPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ultra-zen", "free-pool.json")
}

func validPoolProvider(provider string) bool {
	for _, known := range poolProviders {
		if provider == known {
			return true
		}
	}
	return false
}

func normalizeFreePool(routes []FreeRoute) []FreeRoute {
	out := make([]FreeRoute, 0, len(routes))
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		route.Provider = strings.TrimSpace(route.Provider)
		route.Model = strings.TrimSpace(route.Model)
		if !validPoolProvider(route.Provider) || route.Model == "" || models.IsUnavailable(route.Provider, route.Model) {
			continue
		}
		key := selKey(route.Provider, route.Model)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, route)
	}
	return out
}

// LoadFreePool returns the last pool saved by the TUI, in selection order.
// Missing or corrupt state is treated as no configured pool so startup can
// always continue.
func LoadFreePool() []FreeRoute {
	path := freePoolPath()
	if path == "" {
		return nil
	}
	guard := flock.Lock(path)
	defer guard.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var routes []FreeRoute
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil
	}
	return normalizeFreePool(routes)
}

// SaveFreePool atomically persists the ordered TUI pool. An empty pool clears
// the saved selection, making the reset key survive future launches too.
func SaveFreePool(routes []FreeRoute) error {
	path := freePoolPath()
	if path == "" {
		return fmt.Errorf("cannot resolve config directory")
	}
	routes = normalizeFreePool(routes)
	if len(routes) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".free-pool-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
