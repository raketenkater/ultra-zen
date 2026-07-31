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
	ID   string // gateway model id, e.g. "glm-5.1"
	Name string // human-friendly name
	Base string // gateway base URL this model lives on
	Free bool   // whether the model is a *-free variant
}

// List fetches all usable models for the given API key: every model on the
// opencode-go tier, plus the free models on the main tier. Non-free main-tier
// models are excluded because they require a separate balance the opencode-go
// key does not cover.
func List(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	goModels, goErr := fetchIDs(httpClient, GoBase, apiKey)
	mainModels, mainErr := fetchIDs(httpClient, MainBase, apiKey)
	if goErr != nil && mainErr != nil {
		return nil, fmt.Errorf("go tier: %v; main tier: %w", goErr, mainErr)
	}

	var out []Model
	for _, id := range goModels {
		out = append(out, Model{ID: id, Name: pretty(id), Base: GoBase, Free: false})
	}
	for _, id := range mainModels {
		if !strings.HasSuffix(id, "-free") {
			continue
		}
		out = append(out, Model{ID: id, Name: pretty(id), Base: MainBase, Free: true})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free // free tier first, paid go-tier second
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ListZenFree fetches only opencode's main free tier. It is used when Zen is
// an alternate provider for an OpenRouter-first session, so an unavailable or
// unfunded opencode-go endpoint cannot hide otherwise usable *-free models.
func ListZenFree(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	ids, err := fetchIDs(httpClient, MainBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("main free tier: %w", err)
	}
	var out []Model
	for _, id := range ids {
		if strings.HasSuffix(id, "-free") {
			out = append(out, Model{ID: id, Name: pretty(id), Base: MainBase, Free: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func fetchIDs(c *http.Client, base, key string) ([]string, error) {
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
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// pretty turns a model id into a display name. We keep it close to the id but
// tidy a couple of common suffixes.
func pretty(id string) string {
	name := id
	name = strings.TrimSuffix(name, "-free")
	return name
}

// ListOpenRouter fetches all free models available via OpenRouter. The
// :free models and the openrouter/free router are returned; paid models are
// omitted since ultra-zen is about free/cheap access.
func ListOpenRouter(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	ids, err := fetchIDs(httpClient, OpenRouterBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	var out []Model
	for _, id := range ids {
		if strings.Contains(id, ":free") || id == "openrouter/free" {
			out = append(out, Model{
				ID:   id,
				Name: pretty(id),
				Base: OpenRouterBase,
				Free: true,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ListCodex fetches the model list from a local Codex-compatible endpoint
// (e.g. ChatMock, an OAuth bridge that serves OpenAI-compatible chat/completions
// backed by a ChatGPT Plus/Pro subscription). The endpoint already proxies
// OpenAI models, so every id it advertises is usable.
func ListCodex(httpClient *http.Client, baseURL, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	ids, err := fetchIDs(httpClient, baseURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("codex: %w", err)
	}
	var out []Model
	for _, id := range ids {
		out = append(out, Model{
			ID:   id,
			Name: pretty(id),
			Base: baseURL,
			Free: false, // subscription-backed, not free-tier
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ListFreeTier fetches the model list from a free-tier OpenAI-compatible
// endpoint (see FreeTierProviders). Every model it advertises is treated as
// free, since these are personal-signup free tiers rather than gateways with
// a mix of free and paid models.
func ListFreeTier(httpClient *http.Client, base, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	ids, err := fetchIDs(httpClient, base, apiKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", base, err)
	}
	var out []Model
	for _, id := range ids {
		out = append(out, Model{ID: id, Name: pretty(id), Base: base, Free: true})
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

// Find returns the model with the given id, or nil.
func Find(list []Model, id string) *Model {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}
