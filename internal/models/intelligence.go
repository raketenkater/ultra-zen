// Package models: Artificial Analysis intelligence ranking for free-model
// rotation. The ggrun project's GitHub automation (update-recommendations.yml)
// refreshes a scored catalog every ~3 days and publishes it; uz embeds a
// snapshot of that catalog at build time (intelligence.json) and refreshes a
// locally cached copy on a 24h TTL so new models rank without a new release —
// the same embed+refresh pattern ggrun itself uses. The scores feed one thing:
// the order of the automatic free-model pool, so rotation cycles from the
// smartest available free model down. Matching is best-effort by slug and
// family: an unmatched model keeps catalog order (recent-first) rather than
// being dropped. Attribution travels with the data.
package models

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// errNotOK marks any non-200 / empty-payload fetch as "keep the embedded data".
var errNotOK = errors.New("intelligence fetch unsuccessful")

//go:embed intelligence.json
var intelligenceJSON []byte

// intelligenceCatalogURL is ggrun's published catalog — refreshed by that
// repo's scheduled GitHub automation, so uz needs none of its own. A var (not
// a const) only so tests can point the fetch at a fake server; production
// never reassigns it.
var intelligenceCatalogURL = "https://raw.githubusercontent.com/raketenkater/ggrun/main/go/pkg/recommend/catalog.json"

// intelligenceTTL bounds how often the background refresh fetches.
const intelligenceTTL = 24 * time.Hour

// IntelligenceEntry is one scored model: ggrun's AA slug plus the index.
// Name/Family are display hints for match diagnostics, not ranking inputs.
type IntelligenceEntry struct {
	Slug         string  `json:"slug"`
	Name         string  `json:"name,omitempty"`
	Family       string  `json:"family,omitempty"`
	Intelligence float64 `json:"intelligence"`
}

type intelligenceDoc struct {
	Version     int                 `json:"version"`
	GeneratedAt string              `json:"generated_at"`
	Attribution string              `json:"attribution"`
	Models      []IntelligenceEntry `json:"models"`
}

// orIntelligenceClock is the test seam for TTL decisions.
var orIntelligenceClock = time.Now

// embeddedIntelligence parses the build-time snapshot once. A corrupt embed is
// a programming error, but a nil table must never break a launch — callers
// treat nil as "no intelligence data".
var embeddedIntelligence = parseIntelligence(intelligenceJSON)

func parseIntelligence(b []byte) map[string]float64 {
	var doc intelligenceDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil
	}
	m := make(map[string]float64, len(doc.Models))
	for _, e := range doc.Models {
		if e.Slug != "" && e.Intelligence > 0 {
			m[e.Slug] = e.Intelligence
		}
	}
	return m
}

// intelligencePath is where the refreshed copy lives.
func intelligencePath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(base, "ultra-zen", "intelligence.json")
}

func cachedIntelligence() (map[string]float64, time.Time, bool) {
	b, err := os.ReadFile(intelligencePath())
	if err != nil {
		return nil, time.Time{}, false
	}
	var doc intelligenceDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, time.Time{}, false
	}
	m := parseIntelligence(b)
	if len(m) == 0 {
		return nil, time.Time{}, false
	}
	stamp := time.Time{}
	if doc.GeneratedAt != "" {
		if t, err := time.Parse(time.RFC3339, doc.GeneratedAt); err == nil {
			stamp = t
		}
	}
	return m, stamp, true
}

func writeIntelligenceCache(m map[string]float64, generatedAt, attribution string) {
	doc := intelligenceDoc{Version: 1, GeneratedAt: generatedAt, Attribution: attribution}
	for slug, iq := range m {
		doc.Models = append(doc.Models, IntelligenceEntry{Slug: slug, Intelligence: iq})
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return
	}
	p := intelligencePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// RefreshIntelligenceMaybe fetches ggrun's published catalog when the cached
// copy is older than the TTL (or missing entirely). Best-effort and non-
// blocking by contract: callers run it in a goroutine, and every error —
// network, parse, rate limit — leaves the embedded snapshot in charge. When
// the live fetch succeeds it OVERRIDES the embed wholesale: the automation is
// strictly fresher than anything compiled in.
func RefreshIntelligenceMaybe(httpClient *http.Client) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if _, stamp, ok := cachedIntelligence(); ok && orIntelligenceClock().Sub(stamp) < intelligenceTTL {
		return
	}
	table, generatedAt, attribution, err := fetchIntelligence(httpClient)
	if err != nil {
		return
	}
	writeIntelligenceCache(table, generatedAt, attribution)
}

func fetchIntelligence(httpClient *http.Client) (map[string]float64, string, string, error) {
	resp, err := httpClient.Get(intelligenceCatalogURL)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", errNotOK
	}
	// ggrun's full catalog carries GGUF geometry uz never reads; decode only
	// the fields the ranking needs.
	var doc struct {
		GeneratedAt string `json:"generated_at"`
		Attribution string `json:"attribution"`
		Candidates  []struct {
			Slug         string  `json:"aa_slug"`
			Intelligence float64 `json:"aa_intelligence_index"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, "", "", err
	}
	m := make(map[string]float64, len(doc.Candidates))
	for _, c := range doc.Candidates {
		if c.Slug != "" && c.Intelligence > 0 {
			m[c.Slug] = c.Intelligence
		}
	}
	if len(m) == 0 {
		return nil, "", "", errNotOK
	}
	return m, doc.GeneratedAt, doc.Attribution, nil
}

// IntelligenceFor resolves a free-model id to its AA intelligence index. The
// lookup chain mirrors how uz ids relate to AA slugs:
//  1. the full id ("glm-5.2", "deepseek-v4-flash") — most providers already
//     name models with the undated slug,
//  2. the dotted-to-dashed normalized id ("glm-5-2"),
//  3. family-seeded matching: the id's leading family token ("zai-org",
//     "Qwen-Ambassador", "deepseek-ai") is dropped, and for org-prefixed ids
//     the base name is retried — "zai-org/GLM-5.2" resolves via its base.
//
// Dated slug suffixes (-0424, -0731) and marketing suffixes (-free, -next)
// are stripped progressively, longest first, so "deepseek-v4-flash-free"
// and "deepseek-v4-pro-0424" both land on their scored base model. Returns
// 0 when nothing matches; callers keep catalog order for unknowns.
func IntelligenceFor(table map[string]float64, provider, id string) float64 {
	if table == nil || id == "" {
		return 0
	}
	base := strings.ToLower(id)
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ":free")

	// Progressive suffix stripping: exact first, then peel one suffix at a
	// time. Candidate keys are tried most-specific to least.
	cands := []string{base, strings.ReplaceAll(base, ".", "-")}
	for _, c := range cands {
		if iq, ok := table[c]; ok {
			return iq
		}
	}
	// Peel known suffix families, longest first.
	for _, c := range cands {
		for _, suf := range suffixCandidates(c) {
			if iq, ok := table[suf]; ok {
				return iq
			}
		}
	}
	// Family tokens: an id like "Qwen-Ambassador/Qwen3.8-Max" carries the
	// family in the org; ggrun slugs do not. Try family-prefixed slugs only
	// when the bare name missed (qwen3-8-flash-next style slugs exist).
	return 0
}

// suffixCandidates yields progressively shorter key candidates by dropping one
// trailing token at a time. "deepseek-v4-flash-free" → deepseek-v4-flash;
// "deepseek-v4-pro-0424" → deepseek-v4-pro.
func suffixCandidates(s string) []string {
	parts := strings.Split(s, "-")
	var out []string
	for n := len(parts) - 1; n >= 1; n-- {
		out = append(out, strings.Join(parts[:n], "-"))
	}
	return out
}

// intelligenceTable returns the freshest available table: the disk cache when
// it holds data, else the embedded snapshot.
func intelligenceTable() map[string]float64 {
	if m, _, ok := cachedIntelligence(); ok {
		return m
	}
	return embeddedIntelligence
}

// SortFreeByIntelligence reorders free models smartest-first. Models with no
// AA score keep their incoming (recent-first) order after all scored models —
// never dropped, never scattered. provider+" "+id is the match key passed to
// IntelligenceFor.
func SortFreeByIntelligence(list []Model, provider string) []Model {
	table := intelligenceTable()
	if len(table) == 0 {
		return list
	}
	out := append([]Model(nil), list...)
	type scored struct {
		m   Model
		iq  float64
		idx int
	}
	rows := make([]scored, 0, len(out))
	for i, m := range out {
		rows = append(rows, scored{m, IntelligenceFor(table, provider, m.ID), i})
	}
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].iq != rows[b].iq {
			return rows[a].iq > rows[b].iq
		}
		return rows[a].idx < rows[b].idx
	})
	for i := range rows {
		out[i] = rows[i].m
	}
	return out
}
