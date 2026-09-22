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

// Recent is one recorded launch: the model, and the provider it was reached
// through. The provider is what makes a cross-provider "recently used" list
// possible — a bare id cannot be relaunched, because the same name is served
// by more than one gateway at different prices, and some ids exist on only
// one of them. Provider is empty for entries written before it was recorded.
type Recent struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
}

// LoadRecentRoutes returns the recorded launches, most recent first.
//
// It reads both the current object form and the original bare-string form
// ("glm-5.2"), because the file on disk predates the provider field and
// dropping those entries would silently empty every existing user's recents
// on upgrade. Legacy entries come back with an empty Provider.
func LoadRecentRoutes() []Recent {
	b, err := os.ReadFile(recentPath())
	if err != nil {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}
	out := make([]Recent, 0, len(raw))
	for _, item := range raw {
		var id string
		if err := json.Unmarshal(item, &id); err == nil {
			if id != "" {
				out = append(out, Recent{Model: id})
			}
			continue
		}
		var r Recent
		if err := json.Unmarshal(item, &r); err == nil && r.Model != "" {
			out = append(out, r)
		}
	}
	return out
}

// LoadRecent returns the recorded model ids, most recently used first. It is
// the id-only view of LoadRecentRoutes, kept because the ordering helpers
// (SortByRecent and its callers) rank on ids alone.
func LoadRecent() []string {
	routes := LoadRecentRoutes()
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Model)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RecordRecent moves id to the front of the MRU list (deduped) and persists
// it, without a provider. Prefer RecordRecentRoute, which records where the
// model was reached.
func RecordRecent(id string) { RecordRecentRoute("", id) }

// RecordRecentRoute moves a (provider, model) launch to the front of the MRU
// list and persists it. Errors are swallowed: failing to remember a pick must
// not stop a launch.
//
// Deduplication is by model id, not by the pair. The list answers "what did I
// run recently", and the same model reached two ways is one answer to that —
// keeping both would spend the section's few rows restating one model. The
// newest entry wins, so the provider shown is the one most recently used.
func RecordRecentRoute(provider, model string) {
	if model == "" {
		return
	}
	existing := LoadRecentRoutes()
	out := make([]Recent, 0, len(existing)+1)
	out = append(out, Recent{Provider: provider, Model: model})
	for _, r := range existing {
		if r.Model != model {
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

// Combo is an orchestrator/worker pairing the user launched.
type Combo struct {
	Orchestrator string `json:"orchestrator"`
	Worker       string `json:"worker"`
}

// combosPath is where recently used combos are stored.
func combosPath() string {
	return filepath.Join(filepath.Dir(recentPath()), "recent-combos.json")
}

// LoadCombos returns the recorded orchestrator/worker combos, most recent first.
func LoadCombos() []Combo {
	b, err := os.ReadFile(combosPath())
	if err != nil {
		return nil
	}
	var cs []Combo
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil
	}
	return cs
}

// RecordCombo moves the (orch, worker) pairing to the front and persists it.
// A worker identical to the orchestrator is recorded as no worker: the two
// would render as the same row in the picker and the model row already covers
// that case.
func RecordCombo(orchestrator, worker string) {
	if orchestrator == "" {
		return
	}
	if worker == orchestrator {
		worker = ""
	}
	combos := LoadCombos()
	out := make([]Combo, 0, len(combos)+1)
	out = append(out, Combo{Orchestrator: orchestrator, Worker: worker})
	for _, c := range combos {
		if c.Orchestrator == orchestrator && c.Worker == worker {
			continue
		}
		out = append(out, c)
	}
	if len(out) > maxRecent {
		out = out[:maxRecent]
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	p := combosPath()
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	_ = os.WriteFile(p, b, 0644)
}

// RecommendedCombos are curated orchestrator/worker pairings. The orchestrator
// does the planning (needs a strong reasoning model); the worker fans out
// background sub-agents (wants a cheap, high-rate-limit model). These reflect
// common opencode Zen / OpenRouter practice as of mid-2026.
var RecommendedCombos = []Combo{
	// Paid Go-tier — smart Go orchestrator + cheap Go worker
	{Orchestrator: "glm-5.2", Worker: "deepseek-v4-flash"},
	{Orchestrator: "kimi-k3", Worker: "deepseek-v4-flash"},
	{Orchestrator: "glm-5.2", Worker: "minimax-m2.5"},
	{Orchestrator: "kimi-k2.7-code", Worker: "deepseek-v4-flash"},
	{Orchestrator: "glm-5.2", Worker: "minimax-m3"},
	// Free — orchestrator + free worker
	{Orchestrator: "deepseek-v4-flash-free", Worker: "deepseek-v4-flash-free"},
	{Orchestrator: "qwen/qwen3-coder:free", Worker: "deepseek-v4-flash-free"},
	{Orchestrator: "north-mini-code:free", Worker: "deepseek-v4-flash-free"},
	// OpenRouter free tier — rotates daily; these two have held up well as of
	// mid-2026 (Laguna S 2.1: 118B-A8B coding agent model; Nemotron 3 Ultra:
	// 550B-A55B MoE, 1M context). If either falls off OpenRouter's free list,
	// this entry just stops matching and disappears from the picker.
	{Orchestrator: "poolside/laguna-s-2.1:free", Worker: "poolside/laguna-s-2.1:free"},
	{Orchestrator: "nvidia/nemotron-3-ultra-550b-a55b:free", Worker: "nvidia/nemotron-3-ultra-550b-a55b:free"},
	// ModelScope API-Inference free tier — near-frontier open models, ~2,000
	// calls/day total, ~500/model/day. Shown only when --provider modelscope.
	{Orchestrator: "deepseek-ai/DeepSeek-V4-Pro", Worker: "deepseek-ai/DeepSeek-V4-Flash"},
	{Orchestrator: "ZhipuAI/GLM-5.2", Worker: "deepseek-ai/DeepSeek-V4-Flash"},
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
