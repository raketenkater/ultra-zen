// Package models discovers the models available on the opencode Zen gateway.
// The gateway exposes two tiers: the "go" tier (https://opencode.ai/zen/go/v1,
// the opencode-go provider, credits required) and the main tier
// (https://opencode.ai/zen/v1, which also hosts the *-free models).
package models

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/raketenkater/ultra-zen/internal/keys"
)

// Base URLs for the supported providers.
const (
	GoBase           = "https://opencode.ai/zen/go/v1"
	MainBase         = "https://opencode.ai/zen/v1"
	OpenRouterBase   = "https://openrouter.ai/api/v1"
	GroqBase         = "https://api.groq.com/openai/v1"
	CerebrasBase     = "https://api.cerebras.ai/v1"
	HuggingFaceBase  = "https://router.huggingface.co/v1"
	CohereBase       = "https://api.cohere.ai/compatibility/v1"
	ModelScopeBase   = "https://api-inference.modelscope.ai/v1"
	ModelScopeCNBase = "https://api-inference.modelscope.cn/v1"
	SAIABase         = "https://chat-ai.academiccloud.de/v1"
	// CodexSubBase is the ChatGPT subscription backend the codex CLI talks to.
	// It serves the OpenAI Responses API (POST /responses) and a model catalog
	// at GET /models — NOT chat/completions. There is deliberately no /v1
	// segment; the codex binary's own constant is exactly this string.
	CodexSubBase = "https://chatgpt.com/backend-api/codex"
)

// FreeTierProvider describes a BYO-key OpenAI-compatible endpoint that offers
// a free usage tier, beyond the opencode Zen gateway ultra-zen defaults to.
// Each requires its own personal API key (there is no shared gateway key for
// these, unlike opencode Zen), so ultra-zen reads it from --api-key or the
// provider's own env var the same way it already does for OpenRouter.
type FreeTierProvider struct {
	Base    string // OpenAI-compatible base URL (must serve GET {Base}/models)
	EnvKey  string // environment variable ultra-zen checks for a key
	KeyHint string // where to get a key, shown in prompts/errors
}

// FreeTierProviders lists the additional free-tier providers ultra-zen can
// pull models from via --provider <name>. Sourced from the community-curated
// free-tier list at github.com/cheahjs/free-llm-api-resources, restricted to
// providers that actually expose an OpenAI-compatible GET /models endpoint
// (confirmed live; several well-known free tiers, e.g. Gemini's OpenAI-compat
// layer, do not implement model listing and so are not usable here without a
// hardcoded model list, which would drift out of sync silently).
var FreeTierProviders = map[string]FreeTierProvider{
	"groq":        {Base: GroqBase, EnvKey: "GROQ_API_KEY", KeyHint: "https://console.groq.com/keys"},
	"cerebras":    {Base: CerebrasBase, EnvKey: "CEREBRAS_API_KEY", KeyHint: "https://cloud.cerebras.ai/platform/apikeys"},
	"huggingface": {Base: HuggingFaceBase, EnvKey: "HF_TOKEN", KeyHint: "https://huggingface.co/settings/tokens"},
	"cohere":      {Base: CohereBase, EnvKey: "COHERE_API_KEY", KeyHint: "https://dashboard.cohere.com/api-keys"},
	"modelscope":  {Base: ModelScopeBase, EnvKey: "MODELSCOPE_API_KEY", KeyHint: "https://modelscope.ai/my/myaccesstoken"},
	"saia":        {Base: SAIABase, EnvKey: "SAIA_API_KEY", KeyHint: "https://saia.gwdg.de/dashboard"},
}

// ProviderKey resolves the API key ultra-zen uses for a free-pool provider,
// in the same precedence main.go applies for the primary provider:
// explicit flag > env var > persistent key store. flagKey is the caller's
// already-resolved value for the shared --api-key / --openrouter-key flag;
// zenKey is the already-resolved opencode-go key (from opencode auth.json).
// Returns "" when no key is available.
func ProviderKey(provider, flagKey, zenKey string) string {
	switch provider {
	case "openrouter":
		if flagKey != "" {
			return flagKey
		}
		if os.Getenv("OPENROUTER_API_KEY") != "" {
			return os.Getenv("OPENROUTER_API_KEY")
		}
		return keys.Load("openrouter")
	case "opencode-go":
		if zenKey != "" {
			return zenKey
		}
		return keys.Load("opencode-go")
	default:
		// BYO-key free-tier provider (modelscope/groq/cerebras/huggingface/cohere).
		if flagKey != "" {
			return flagKey
		}
		if def, ok := FreeTierProviders[provider]; ok && os.Getenv(def.EnvKey) != "" {
			return os.Getenv(def.EnvKey)
		}
		return keys.Load(provider)
	}
}

// Model is one selectable model.
type Model struct {
	ID            string // gateway model id, e.g. "glm-5.1"
	Name          string // human-friendly name
	Base          string // gateway base URL this model lives on
	Free          bool   // whether the model is a *-free variant
	ContextLength int    // maximum context length in tokens (0 = unknown)
	// CanonicalSlug is the model's canonical_slug from the gateway /models
	// endpoint (e.g. "deepseek/deepseek-v4-flash-20260731"). It is undated for
	// some models and dated for others; use normalizeSlug to match it against
	// ranking permaslugs. Empty when the gateway does not report it.
	CanonicalSlug string
}

// List fetches all usable models for the given API key: every model on the
// opencode-go tier, plus the free models on the main tier. Non-free main-tier
// models are excluded because they require a separate balance the opencode-go
// key does not cover.
func List(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	goEntries, goErr := fetchEntries(httpClient, GoBase, apiKey)
	mainEntries, mainErr := fetchEntries(httpClient, MainBase, apiKey)
	if goErr != nil && mainErr != nil {
		return nil, fmt.Errorf("go tier: %v; main tier: %w", goErr, mainErr)
	}

	var out []Model
	for _, e := range goEntries {
		out = append(out, Model{ID: e.ID, Name: pretty(e.ID), Base: GoBase, Free: false, ContextLength: e.ContextLength})
	}
	for _, e := range mainEntries {
		if !strings.HasSuffix(e.ID, "-free") {
			continue
		}
		out = append(out, Model{ID: e.ID, Name: pretty(e.ID), Base: MainBase, Free: true, ContextLength: e.ContextLength})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return !out[i].Free // paid go-tier first, free tier second
		}
		return out[i].Name < out[j].Name
	})
	return FilterUnavailable("opencode-go", out), nil
}

// ListZenFree fetches only opencode's main free tier. It is used when Zen is
// an alternate provider for an OpenRouter-first session, so an unavailable or
// unfunded opencode-go endpoint cannot hide otherwise usable *-free models.
func ListZenFree(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	entries, err := fetchEntries(httpClient, MainBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("main free tier: %w", err)
	}
	var out []Model
	for _, e := range entries {
		if strings.HasSuffix(e.ID, "-free") {
			out = append(out, Model{ID: e.ID, Name: pretty(e.ID), Base: MainBase, Free: true, ContextLength: e.ContextLength})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return FilterUnavailable("opencode-go", out), nil
}

// apiModelEntry is one model returned by the gateway's GET /models endpoint.
// We read context_length from the metadata so autocompaction can be set from
// the model's real context window instead of a hardcoded guess.
type apiModelEntry struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	CanonicalSlug string `json:"canonical_slug"`
}

// normalizeSlug strips a trailing -YYYYMMDD date suffix from a model slug so
// that the dated canonical_slugs used in the rankings-daily dataset
// (e.g. "deepseek/deepseek-v4-flash-20260731") and the undated /models ids
// (e.g. "deepseek/deepseek-v4-flash") can be matched. It also trims whitespace.
// It does NOT touch the existing ":free"/":batch" variant markers.
func normalizeSlug(s string) string {
	s = strings.TrimSpace(s)
	if n := len(s); n > 9 && s[n-9] == '-' {
		// candidate suffix is exactly 8 ASCII digits
		suffix := s[n-8:]
		ok := true
		for i := 0; i < 8; i++ {
			if suffix[i] < '0' || suffix[i] > '9' {
				ok = false
				break
			}
		}
		if ok {
			s = s[:n-9]
		}
	}
	return s
}


// knownContextWindows supplies the real context window for models whose
// gateway's /models endpoint does NOT report one. Verified live: the opencode
// Zen gateway (go + main tiers) returns entries with only id/object/created/
// owned_by — no context_length, no context_window. Without this table every
// Zen model falls back to contextWindowDefault (200k), which badly
// under-reports 1M-window models (deepseek-v4-flash, minimax-m3) and can
// over-report smaller ones, so Claude Code autocompacts at the wrong point and
// the conversation overflows the gateway's true limit. Values cross-checked
// against live OpenRouter / models + provider docs (see the autocompact
// diagnosis workflow, 2026-08-16).
var knownContextWindows = map[string]int{
	// DeepSeek V4 — 1M context.
	"deepseek-v4-flash": 1_000_000,
	"deepseek-v4-pro":   1_000_000,
	// GLM-5 family — 200k class (GLM-5.2 is 1M on OpenRouter, but the Zen
	// gateway serves the 200k tier; a safe floor avoids overflow).
	"glm-5":   200_000,
	"glm-5.1": 200_000,
	"glm-5.2": 200_000,
	"glm-5.3": 200_000,
	// Kimi K2 family — 256k; K3 is 1M.
	"kimi-k2.5":      256_000,
	"kimi-k2.6":      256_000,
	"kimi-k2.7-code": 256_000,
	"kimi-k3":        1_000_000,
	// MiniMax — M2.x 200k, M3 1M.
	"minimax-m2.5": 204_800,
	"minimax-m2.7": 204_800,
	"minimax-m3":   1_000_000,
	// Mimo (Moonshot) — 200k class.
	"mimo-v2.5":     200_000,
	"mimo-v2.5-pro": 200_000,
	"mimo-v2-omni":  200_000,
	"mimo-v2-pro":   200_000,
	// OpenAI GPT-5.6 on the Zen gateway — 272k.
	"gpt-5.6-luna":  272_000,
	"gpt-5.6-terra": 272_000,
	// xAI Grok — 500k.
	"grok-4.5": 500_000,
	// Qwen3 Coder Next — 256k context (SAIA catalog omits context metadata).
	"qwen3-coder-next": 262_144,
	// Hyperbolic/other — conservative 200k.
	"hy3": 200_000,
}

func fetchEntries(c *http.Client, base, key string) ([]apiModelEntry, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s/models: %s: %s", base, resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data []apiModelEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}
	sort.SliceStable(payload.Data, func(i, j int) bool { return payload.Data[i].ID < payload.Data[j].ID })
	fillContextWindows(payload.Data)
	return payload.Data, nil
}

// fillContextWindows fills in curated context windows for any entry the
// gateway left at 0 (the Zen/ModelScope gateways report no context). Real
// gateway values are never overwritten. The table is keyed by gateway model id
// (e.g. "deepseek-v4-flash"), which matches the Zen and ModelScope catalogs.
func fillContextWindows(entries []apiModelEntry) {
	for i := range entries {
		if entries[i].ContextLength > 0 {
			continue
		}
		if n, ok := knownContextWindows[entries[i].ID]; ok {
			entries[i].ContextLength = n
		}
	}
}

// fetchIDs is kept for tests that only need ID strings.
func fetchIDs(c *http.Client, base, key string) ([]string, error) {
	entries, err := fetchEntries(c, base, key)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids, nil
}

// pretty turns a model id into a display name. It strips the -free suffix;
// most catalog names are derived through FriendlyName instead.
func pretty(id string) string {
	return FriendlyName(id)
}

// friendlyTokens maps vendor/model tokens to their canonical, human-readable
// casing. Everything not listed is title-cased.
var friendlyTokens = map[string]string{
	"gpt":          "GPT",
	"glm":          "GLM",
	"qwen":         "Qwen",
	"qwen3":        "Qwen3",
	"qwen3.5":      "Qwen3.5",
	"minimax":      "MiniMax",
	"moonshot":     "Moonshot",
	"deepseek":     "DeepSeek",
	"kimi":         "Kimi",
	"grok":         "Grok",
	"cohere":       "Cohere",
	"gemma":        "Gemma",
	"nemotron":     "Nemotron",
	"laguna":       "Laguna",
	"mimo":         "Mimo",
	"north":        "North",
	"ernie":        "ERNIE",
	"paddlepaddle": "PaddlePaddle",
	"internvl":     "InternVL",
	"openrouter":   "OpenRouter",
	"free":         "Free",
	"code":         "Code",
	"reasoning":    "Reasoning",
	"omni":         "Omni",
	"flash":        "Flash",
	"pro":          "Pro",
	"mini":         "Mini",
	"nano":         "Nano",
	"lightning":    "Lightning",
	"super":        "Super",
	"ultra":        "Ultra",
	"it":           "IT",
	"oss":          "OSS",
	"xs":           "XS",
	"luna":         "Luna",
	"sol":          "Sol",
	"terra":        "Terra",
	"preview":      "Preview",
	"thinking":     "Thinking",
	"instruct":     "Instruct",
	"tool":         "Tool",
	"coder":        "Coder",
}

// FriendlyName turns a gateway model id into a human-readable display name so
// the /model picker and TUI identify models at a glance. It is a pure function
// of the id — the gateways mostly don't supply friendly names (only codex-sub
// does, and that path uses the real DisplayName instead). Rules:
//
//   - strip the ":free" / "-free" suffix;
//   - drop an owner/org prefix (ModelScope/HF style "zai-org/GLM-5.2");
//   - split on '/', ':', '-', '_' and title-case, keeping known vendor tokens
//     in their canonical casing (GLM, Qwen, MiniMax, ...) and size tokens
//     uppercase (26B, A22B);
//   - "openrouter/free" -> "OpenRouter Free".
//
// Examples:
//
//	deepseek-v4-flash          -> DeepSeek V4 Flash
//	kimi-k2.6                  -> Kimi K2.6
//	poolside/laguna-s-2.1:free -> Laguna S 2.1
//	zai-org/GLM-5.2            -> GLM 5.2
//	Qwen/Qwen3-235B-A22B       -> Qwen3 235B A22B
func FriendlyName(id string) string {
	name := strings.TrimSuffix(id, ":free")
	name = strings.TrimSuffix(name, "-free")
	// openrouter/free is a router pseudo-model.
	if name == "openrouter/free" {
		return "OpenRouter Free"
	}
	// Drop an owner/org prefix (ModelScope/HF style "org/Model").
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	// Split into tokens on separators. Digits and dots stay attached to the
	// token they're in (K2.6, v4, 5.2), and a letter right after digits also
	// stays attached (26B, A22B), so sizes keep their unit.
	var tokens []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range name {
		switch {
		case r == '-' || r == '_' || r == '/':
			flush()
		case r == ' ':
			flush()
		default:
			// Every non-separator rune joins the current token, so digits and
			// dots stay attached to their word (K2.6, v4, 5.2, 26b, A22B) and a
			// token is only ever split on '-'/'_'/'/'/space.
			cur.WriteRune(r)
		}
	}
	flush()

	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		// Canonical casing for known tokens, else title-case.
		if canon, ok := friendlyTokens[strings.ToLower(tok)]; ok {
			out = append(out, canon)
			continue
		}
		// Size tokens (26B, A22B, 235B, 12b -> 12B) uppercase.
		if isSizeToken(tok) {
			out = append(out, strings.ToUpper(tok))
			continue
		}
		out = append(out, titleWord(tok))
	}
	if len(out) == 0 {
		return id
	}
	return strings.Join(out, " ")
}

// isSizeToken reports whether a token is a model size/variant like 26B, A22B,
// 235B, 1b, a4b — keep these uppercase so tiers stay distinguishable.
func isSizeToken(tok string) bool {
	lower := strings.ToLower(tok)
	if len(lower) < 2 || lower[len(lower)-1] != 'b' {
		return false
	}
	// The token ends in "b"; the prefix must contain at least one digit and
	// otherwise be letters/digits (26b, a4b, 235b, A22b). "flash" -> false.
	hasDigit := false
	for _, r := range lower[:len(lower)-1] {
		if r >= '0' && r <= '9' {
			hasDigit = true
		} else if r < 'a' || r > 'z' {
			return false
		}
	}
	return hasDigit
}

// titleWord title-cases a single word: "deepseek" -> "Deepseek", "GLM" stays
// "GLM" only via the token table; here plain alpha words get their first letter
// capitalised.
func titleWord(word string) string {
	if word == "" {
		return word
	}
	// Preserve words that are already all-caps (A22B handled by isSizeToken).
	lower := strings.ToLower(word)
	if word == lower {
		return strings.ToUpper(word[:1]) + word[1:]
	}
	return word
}

// ListOpenRouter fetches all free models available via OpenRouter. The
// :free models and the openrouter/free router are returned; paid models are
// omitted since ultra-zen is about free/cheap access.
func ListOpenRouter(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	entries, err := fetchEntries(httpClient, OpenRouterBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	var out []Model
	for _, e := range entries {
		if strings.Contains(e.ID, ":free") || e.ID == "openrouter/free" {
			out = append(out, Model{
				ID:            e.ID,
				Name:          pretty(e.ID),
				Base:          OpenRouterBase,
				Free:          true,
				ContextLength: e.ContextLength,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return FilterUnavailable("openrouter", out), nil
}

// ListOpenRouterAll fetches EVERY model available via OpenRouter (paid + free),
// unlike ListOpenRouter which keeps only the :free models. Free is derived from
// the id (":free" suffix or the openrouter/free router). Used by the
// --all-models flag to expose the complete catalog. The default ListOpenRouter
// behavior is unchanged.
func ListOpenRouterAll(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	entries, err := fetchEntries(httpClient, OpenRouterBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	var out []Model
	for _, e := range entries {
		out = append(out, Model{
			ID:            e.ID,
			Name:          pretty(e.ID),
			Base:          OpenRouterBase,
			Free:          strings.Contains(e.ID, ":free") || e.ID == "openrouter/free",
			ContextLength: e.ContextLength,
			CanonicalSlug: e.CanonicalSlug,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return FilterUnavailable("openrouter", out), nil
}

// ListOpenRouterRanked fetches every OpenRouter model and orders them by real
// current usage (total tokens over the last 7 days) using the OpenRouter
// rankings-daily dataset. Free models form the first block (usage-descending
// within the block), paid models follow (usage-descending), so the top of a
// caller's TopN cap is always the free tier — raw token counts alone left free
// models scattered mid-list or cut off entirely behind paid volume. Models
// absent from the rankings fall to the end of their block, alphabetically.
// The returned slice is the full catalog in that order. Requires an API key.
func ListOpenRouterRanked(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	entries, err := fetchEntries(httpClient, OpenRouterBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	// Base models from /models.
	var out []Model
	for _, e := range entries {
		out = append(out, Model{
			ID:            e.ID,
			Name:          pretty(e.ID),
			Base:          OpenRouterBase,
			Free:          strings.Contains(e.ID, ":free") || e.ID == "openrouter/free",
			ContextLength: e.ContextLength,
			CanonicalSlug: e.CanonicalSlug,
		})
	}
	rank := fetchOpenRouterRanking(httpClient, apiKey)
	orderOpenRouterRanked(out, rank)
	return FilterUnavailable("openrouter", out), nil
}

// orderOpenRouterRanked sorts out in place: free block first, paid second,
// usage-descending within each block, alphabetical for unranked or tied
// models. Kept as a pure function so the grouping is unit-testable without a
// live catalog or rankings endpoint.
func orderOpenRouterRanked(out []Model, rank map[string]rankingEntry) {
	// rankOf resolves a usage rank for a model. The rankings-daily dataset keys
	// rows by model_permaslug, which for some models is the bare /models id and
	// for others the dated canonical_slug (e.g. "...-20260731"). Normalize both
	// sides by stripping the trailing -YYYYMMDD date before matching so dated
	// high-volume models rank by their real volume instead of falling to the
	// bottom as unranked.
	rankOf := func(m Model) rankingEntry {
		if e, ok := rank[normalizeSlug(m.ID)]; ok {
			return e
		}
		if m.CanonicalSlug != "" {
			if e, ok := rank[normalizeSlug(m.CanonicalSlug)]; ok {
				return e
			}
		}
		return rankingEntry{}
	}
	// Stable sort: free first, then usage-desc within each block. Keeping free
	// models in one top block means a TopN(100) cap can never truncate the free
	// tier behind paid traffic (a :free model's tokens count separately from
	// its paid twin).
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free // free first
		}
		ri, rj := rankOf(out[i]), rankOf(out[j])
		if ri.tokens != rj.tokens {
			return ri.tokens > rj.tokens // higher usage first
		}
		return out[i].Name < out[j].Name
	})
}

// rankingEntry holds an aggregated usage rank for one model.
type rankingEntry struct {
	tokens int64
}

// fetchOpenRouterRanking queries /datasets/rankings-daily across a trailing
// ~7-day window and aggregates total_tokens per model_permaslug, giving a
// current-usage ranking (the daily dataset has no separate monthly endpoint).
// A short window keeps recent / latest-model usage dominant so old, high-volume
// but deprecated models don't accumulate to the top. Transport errors or an
// empty dataset return an empty map, leaving models unranked (alpha order).
//
// The ranking rows are keyed by model_permaslug, which uses dated
// canonical_slugs for some models (e.g. "deepseek/deepseek-v4-flash-20260731")
// and the bare id for others. We normalize each permaslug by stripping the
// trailing -YYYYMMDD date so the resulting map keys align with the /models ids
// and canonical_slugs used by ListOpenRouterRanked.
func fetchOpenRouterRanking(httpClient *http.Client, apiKey string) map[string]rankingEntry {
	out := map[string]rankingEntry{}
	now := time.Now()
	start := now.AddDate(0, 0, -7).UTC().Format("2006-01-02")
	end := now.UTC().Format("2006-01-02")
	url := fmt.Sprintf("%s/datasets/rankings-daily?start_date=%s&end_date=%s", OpenRouterBase, start, end)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return out
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return out
	}
	var payload struct {
		Data []struct {
			Model string `json:"model_permaslug"`
			Total string `json:"total_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return out
	}
	for _, row := range payload.Data {
		if row.Model == "" {
			continue
		}
		// Normalize the permaslug: strip any trailing -YYYYMMDD date so it
		// matches the undated /models ids and undated canonical_slugs.
		key := normalizeSlug(row.Model)
		if key == "" {
			continue
		}
		var t int64
		fmt.Sscanf(strings.TrimSpace(row.Total), "%d", &t)
		e := out[key]
		e.tokens += t
		out[key] = e
	}
	return out
}

// TopN returns at most the first n models from a list. Used to cap the default
// OpenRouter picker view to the most-used models.
func TopN(ms []Model, n int) []Model {
	if n <= 0 || len(ms) <= n {
		return ms
	}
	return ms[:n]
}

// ListZenAll fetches BOTH the opencode-go (paid) tier and the main tier
// (free + any non-free main models), unlike List/ListZenFree which restrict to
// free models. Go-tier entries are Free=false; main entries are Free when their
// id ends in "-free". Used by the --all-models flag. The default List/ListZenFree
// behavior is unchanged.
func ListZenAll(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	goEntries, goErr := fetchEntries(httpClient, GoBase, apiKey)
	mainEntries, mainErr := fetchEntries(httpClient, MainBase, apiKey)
	if goErr != nil && mainErr != nil {
		return nil, fmt.Errorf("go tier: %v; main tier: %w", goErr, mainErr)
	}
	var out []Model
	for _, e := range goEntries {
		out = append(out, Model{ID: e.ID, Name: pretty(e.ID), Base: GoBase, Free: false, ContextLength: e.ContextLength})
	}
	for _, e := range mainEntries {
		out = append(out, Model{ID: e.ID, Name: pretty(e.ID), Base: MainBase, Free: strings.HasSuffix(e.ID, "-free"), ContextLength: e.ContextLength})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return !out[i].Free // paid go-tier first, free tier second
		}
		return out[i].Name < out[j].Name
	})
	return FilterUnavailable("opencode-go", out), nil
}

// ListCodex fetches the model list from a local Codex-compatible endpoint
// (e.g. ChatMock, an OAuth bridge that serves OpenAI-compatible chat/completions
// backed by a ChatGPT Plus/Pro subscription). The endpoint already proxies
// OpenAI models, so every id it advertises is usable.
func ListCodex(httpClient *http.Client, baseURL, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	entries, err := fetchEntries(httpClient, baseURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("codex: %w", err)
	}
	var out []Model
	for _, e := range entries {
		out = append(out, Model{
			ID:            e.ID,
			Name:          pretty(e.ID),
			Base:          baseURL,
			Free:          false, // subscription-backed, not free-tier
			ContextLength: e.ContextLength,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// codexModelEntry is one model in the ChatGPT backend's GET /models response.
// The codex backend does NOT speak the OpenAI {data:[{id}]} shape — it returns
// {"models":[{slug,display_name,context_window,visibility,supported_in_api}]}.
type codexModelEntry struct {
	Slug           string `json:"slug"`
	DisplayName    string `json:"display_name"`
	ContextWindow  int    `json:"context_window"`
	Visibility     string `json:"visibility"`
	SupportedInAPI bool   `json:"supported_in_api"`
}

// ListCodexSub fetches the model catalog from the ChatGPT subscription backend
// (the one the installed codex CLI authenticates against). accessToken is sent
// as the Bearer credential and accountID as ChatGPT-Account-ID. Only models the
// API can actually serve (supported_in_api && visibility=="list") are returned,
// mirroring how the codex CLI filters its picker. clientVersion is the codex
// CLI version string the backend expects in the query (e.g. "0.147.0"); pass ""
// to omit the query.
func ListCodexSub(httpClient *http.Client, base, accessToken, accountID, clientVersion string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if clientVersion != "" {
		q := req.URL.Query()
		q.Set("client_version", clientVersion)
		req.URL.RawQuery = q.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex-sub models: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex-sub models: GET %s/models: %s: %s", base, resp.Status, strings.TrimSpace(string(body)))
	}
	return parseCodexModels(body, base)
}

// parseCodexModels parses the codex backend's {"models":[...]} catalog into
// []Model. Entries that are hidden or not API-served are skipped (the codex CLI
// does the same when building its picker). base is the endpoint the catalog was
// fetched from, kept on each Model so the proxy routes requests there.
func parseCodexModels(body []byte, base string) ([]Model, error) {
	var payload struct {
		Models []codexModelEntry `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse codex models: %w", err)
	}
	var out []Model
	for _, e := range payload.Models {
		if e.Slug == "" || !e.SupportedInAPI || e.Visibility != "list" {
			continue
		}
		out = append(out, Model{
			ID:            e.Slug,
			Name:          e.DisplayName,
			Base:          base,
			Free:          false, // subscription-backed, not a free tier
			ContextLength: e.ContextWindow,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CodexModelsCachePath is the codex CLI's own model-cache file. It stores the
// same shape as the live GET /models response, so it doubles as an offline
// fallback when the backend is unreachable.
func CodexModelsCachePath() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(hd, ".codex")
	}
	return filepath.Join(home, "models_cache.json")
}

// ListCodexModelsFromCache reads the codex CLI's cached model catalog (same
// {"models":[...]} shape as the live endpoint). base is the endpoint the models
// are associated with (CodexSubBase). Returns an error when the cache is absent
// or unreadable, so the caller falls back to the live fetch.
func ListCodexModelsFromCache(base string) ([]Model, error) {
	path := CodexModelsCachePath()
	if path == "" {
		return nil, fmt.Errorf("codex models cache: home dir unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("codex models cache (%s): %w", path, err)
	}
	list, err := parseCodexModels(data, base)
	if err != nil {
		return nil, fmt.Errorf("codex models cache (%s): %w", path, err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("codex models cache (%s): no usable models", path)
	}
	return list, nil
}

// ListFreeTier fetches the model list from a free-tier OpenAI-compatible
// endpoint (see FreeTierProviders). Every model it advertises is treated as
// free, since these are personal-signup free tiers rather than gateways with
// a mix of free and paid models.
func ListFreeTier(httpClient *http.Client, base, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	entries, err := fetchEntries(httpClient, base, apiKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", base, err)
	}
	var out []Model
	for _, e := range entries {
		out = append(out, Model{ID: e.ID, Name: pretty(e.ID), Base: base, Free: true, ContextLength: e.ContextLength})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ListFreeTierProvider loads a named BYO-key provider. ModelScope operates two
// independent sites whose tokens are not interchangeable, so try the
// international endpoint first and then China. The successful base is kept on
// each Model, ensuring subsequent proxy requests use the matching site.
func ListFreeTierProvider(httpClient *http.Client, provider, apiKey string) ([]Model, error) {
	def, ok := FreeTierProviders[provider]
	if !ok {
		return nil, fmt.Errorf("unknown free-tier provider %q", provider)
	}
	bases := []string{def.Base}
	if provider == "modelscope" {
		bases = append(bases, ModelScopeCNBase)
	}
	var failures []string
	for _, base := range bases {
		list, err := ListFreeTier(httpClient, base, apiKey)
		if err == nil {
			return FilterUnavailable(provider, list), nil
		}
		failures = append(failures, err.Error())
	}
	return nil, fmt.Errorf("%s endpoints failed: %s", provider, strings.Join(failures, "; "))
}

// ListFreeTierProviderAll loads a named BYO-key provider and keeps EVERY model
// it advertises (paid + free), unlike ListFreeTierProvider which treats every
// entry as free. Free is derived from the id ("-free" suffix or ":free"
// substring). ModelScope's international/China dual-site logic is preserved.
// Used by the --all-models flag. The default ListFreeTierProvider behavior is
// unchanged.
func ListFreeTierProviderAll(httpClient *http.Client, provider, apiKey string) ([]Model, error) {
	def, ok := FreeTierProviders[provider]
	if !ok {
		return nil, fmt.Errorf("unknown free-tier provider %q", provider)
	}
	bases := []string{def.Base}
	if provider == "modelscope" {
		bases = append(bases, ModelScopeCNBase)
	}
	var failures []string
	for _, base := range bases {
		entries, err := fetchEntries(httpClient, base, apiKey)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		var out []Model
		for _, e := range entries {
			out = append(out, Model{
				ID:            e.ID,
				Name:          pretty(e.ID),
				Base:          base,
				Free:          strings.HasSuffix(e.ID, "-free") || strings.Contains(e.ID, ":free"),
				ContextLength: e.ContextLength,
			})
		}
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Name < out[j].Name
		})
		return FilterUnavailable(provider, out), nil
	}
	return nil, fmt.Errorf("%s endpoints failed: %s", provider, strings.Join(failures, "; "))
}

// Find returns the model with the given id, or nil.
func Find(list []Model, id string) *Model {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// BaseForProvider maps a provider name to the gateway base URL its catalog is
// served from, so the re-availability poller knows which /models endpoint to
// re-check when a denial may have lapsed. Returns "" for unknown providers.
func BaseForProvider(provider string) string {
	switch provider {
	case "opencode-go":
		return GoBase
	case "openrouter":
		return OpenRouterBase
	case "groq":
		return GroqBase
	case "cerebras":
		return CerebrasBase
	case "huggingface":
		return HuggingFaceBase
	case "cohere":
		return CohereBase
	case "modelscope":
		return ModelScopeBase
	case "saia":
		return SAIABase
	case "codex":
		// The auto-detected ChatGPT subscription backend (the only codex route
		// the recheck poller can reach with the shared token; a user-specified
		// local endpoint is not a constant base).
		return CodexSubBase
	default:
		return ""
	}
}
