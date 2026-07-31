package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// unavailableModel is a provider/model pair that the provider advertised but
// explicitly rejected as inaccessible for the configured account. Providers
// such as ModelScope do not expose this distinction in GET /models.
type unavailableModel struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	DeniedAt time.Time `json:"denied_at"`
}

var unavailableMu sync.Mutex

func unavailablePath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ultra-zen", "unavailable-models.json")
}

func unavailableKey(provider, model string) string {
	return strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(model)
}

func loadUnavailableLocked() map[string]unavailableModel {
	out := map[string]unavailableModel{}
	path := unavailablePath()
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var entries []unavailableModel
	if json.Unmarshal(data, &entries) != nil {
		return out
	}
	for _, entry := range entries {
		entry.Provider = strings.TrimSpace(entry.Provider)
		entry.Model = strings.TrimSpace(entry.Model)
		if entry.Provider == "" || entry.Model == "" {
			continue
		}
		out[unavailableKey(entry.Provider, entry.Model)] = entry
	}
	return out
}

func saveUnavailableLocked(entries map[string]unavailableModel) error {
	path := unavailablePath()
	if path == "" {
		return fmt.Errorf("cannot resolve config directory")
	}
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	ordered := make([]unavailableModel, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Provider != ordered[j].Provider {
			return ordered[i].Provider < ordered[j].Provider
		}
		return ordered[i].Model < ordered[j].Model
	})
	data, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".unavailable-models-*.tmp")
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

// MarkUnavailable remembers a provider/model pair after an explicit access
// denial. It is then hidden from catalogs and pruned from saved rotations.
func MarkUnavailable(provider, model string) error {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	unavailableMu.Lock()
	defer unavailableMu.Unlock()
	entries := loadUnavailableLocked()
	entries[unavailableKey(provider, model)] = unavailableModel{
		Provider: provider,
		Model:    model,
		DeniedAt: time.Now().UTC(),
	}
	return saveUnavailableLocked(entries)
}

// ClearUnavailable forgets access denials for provider. Setting or clearing a
// provider key calls this so a different account gets a fresh catalog.
func ClearUnavailable(provider string) error {
	provider = strings.TrimSpace(provider)
	unavailableMu.Lock()
	defer unavailableMu.Unlock()
	entries := loadUnavailableLocked()
	for key, entry := range entries {
		if entry.Provider == provider {
			delete(entries, key)
		}
	}
	return saveUnavailableLocked(entries)
}

// IsUnavailable reports whether a provider explicitly rejected this model.
func IsUnavailable(provider, model string) bool {
	unavailableMu.Lock()
	defer unavailableMu.Unlock()
	_, ok := loadUnavailableLocked()[unavailableKey(provider, model)]
	return ok
}

// FilterUnavailable removes models previously rejected for this provider.
func FilterUnavailable(provider string, list []Model) []Model {
	unavailableMu.Lock()
	entries := loadUnavailableLocked()
	unavailableMu.Unlock()
	if len(entries) == 0 {
		return list
	}
	out := make([]Model, 0, len(list))
	for _, model := range list {
		if _, denied := entries[unavailableKey(provider, model.ID)]; !denied {
			out = append(out, model)
		}
	}
	return out
}
