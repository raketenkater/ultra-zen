// Package models: recent.go is a tiny MRU store for the model selector. Each
// time ultra-zen launches a model, the id is recorded; the selector then lists
// recently used models first so repeat picks are one Enter away. The store is
// best-effort — a read/write failure is ignored and falls back to the default
// ordering rather than blocking a launch.
package models

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// maxRecent caps how many ids we keep; older entries drop off the end.
const maxRecent = 10

// recentPath is where the MRU list lives, honouring XDG_CACHE_HOME.
func recentPath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(base, "ultra-zen", "recent-models.json")
}

// LoadRecent returns the recorded model ids, most recently used first.
// Missing/corrupt file yields an empty slice.
func LoadRecent() []string {
	b, err := os.ReadFile(recentPath())
	if err != nil {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return nil
	}
	return ids
}

// RecordRecent moves id to the front of the MRU list (deduped) and persists
// it. Errors are swallowed: failing to remember a pick must not stop a launch.
func RecordRecent(id string) {
	if id == "" {
		return
	}
	ids := LoadRecent()
	out := make([]string, 0, len(ids)+1)
	out = append(out, id)
	for _, r := range ids {
		if r != id {
			out = append(out, r)
		}
	}
	if len(out) > maxRecent {
		out = out[:maxRecent]
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	p := recentPath()
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	_ = os.WriteFile(p, b, 0644)
}

// SortByRecent returns list ordered with recently used models first (in MRU
// order), followed by the rest in their existing relative order. The input is
// not mutated.
func SortByRecent(list []Model, recent []string) []Model {
	if len(recent) == 0 {
		return list
	}
	rank := make(map[string]int, len(recent))
	for i, id := range recent {
		rank[id] = i
	}
	used := make([]Model, 0, len(recent))
	rest := make([]Model, 0, len(list))
	for _, m := range list {
		if _, ok := rank[m.ID]; ok {
			used = append(used, m)
		} else {
			rest = append(rest, m)
		}
	}
	// Order `used` by MRU rank.
	for i := 0; i < len(used); i++ {
		for j := i + 1; j < len(used); j++ {
			if rank[used[j].ID] < rank[used[i].ID] {
				used[i], used[j] = used[j], used[i]
			}
		}
	}
	return append(used, rest...)
}
