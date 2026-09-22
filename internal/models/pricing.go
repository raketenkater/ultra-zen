package models

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Price is what a model costs, normalised to US dollars per one million
// tokens. The gateways publish per-token decimals ("0.00000009"); a number
// that small is unreadable in a picker column, and every published comparison
// quotes $/M, so the conversion happens once here rather than at each render
// site.
//
// Known is the point of the type. A zero price is a real, meaningful value —
// it is what a free model costs — while a provider that publishes no price at
// all (the Zen gateway's /models returns id/object/created/owned_by and
// nothing else) is a different fact entirely. Collapsing the two would print
// "$0.00/M" next to a model that bills against credits, which is an invented
// cost. Callers must branch on Known before reading the numbers.
type Price struct {
	PromptUSD     float64 // per 1M prompt tokens
	CompletionUSD float64 // per 1M completion tokens
	CacheReadUSD  float64 // per 1M cached prompt tokens (0 = not offered)
	Known         bool    // false = this provider publishes no per-token price
}

// Free reports whether this is a published price of zero — genuinely free,
// as opposed to merely unpriced. An unknown price is never free.
func (p Price) Free() bool { return p.Known && p.PromptUSD == 0 && p.CompletionUSD == 0 }

// Short renders the price for a one-line picker row: the prompt (input) rate,
// which is the number that differs most between providers serving the same
// model. Unknown prices render as the caller's fallback word rather than a
// number, so "no published price" never reads as "free".
func (p Price) Short(unknown string) string {
	if !p.Known {
		return unknown
	}
	if p.Free() {
		return "free"
	}
	return "$" + trimUSD(p.PromptUSD) + "/M"
}

// trimUSD formats a dollars-per-million figure at the precision it actually
// carries. Rates span four orders of magnitude (DeepSeek V4 Flash at $0.04/M,
// Opus-class models above $15/M), so a fixed %.2f would print "$0.04" and
// "$0.00" for two providers that differ by 4x at the bottom of the range.
func trimUSD(v float64) string {
	switch {
	case v >= 10:
		return strconv.FormatFloat(v, 'f', 1, 64)
	case v >= 1:
		return strconv.FormatFloat(v, 'f', 2, 64)
	case v >= 0.01:
		return strconv.FormatFloat(v, 'f', 3, 64)
	default:
		// Below a cent per million: keep enough digits to rank providers
		// against each other, then drop the trailing zeros the padding added.
		s := strconv.FormatFloat(v, 'f', 5, 64)
		s = strings.TrimRight(s, "0")
		return strings.TrimSuffix(s, ".")
	}
}

// apiPricing is the pricing object on a gateway /models entry and on each
// endpoint of /models/{id}/endpoints. Values are per-token decimal *strings*
// ("0.00000009"), not numbers — OpenRouter sends them as strings to dodge
// float rounding, and a float64 field here would silently fail to decode.
type apiPricing struct {
	Prompt         string `json:"prompt"`
	Completion     string `json:"completion"`
	InputCacheRead string `json:"input_cache_read"`
}

// price converts the per-token strings to a Price. A pricing object whose
// prompt rate does not parse yields an unknown Price: a half-read price is
// worse than an admitted absence, because it renders as an authoritative
// number.
func (p *apiPricing) price() Price {
	if p == nil {
		return Price{}
	}
	prompt, ok := perTokenToUSDPerM(p.Prompt)
	if !ok {
		return Price{}
	}
	completion, ok := perTokenToUSDPerM(p.Completion)
	if !ok {
		// A prompt rate with no completion rate is still usable — the picker
		// ranks on input cost — but the output column must stay empty rather
		// than mirror the input rate.
		completion = 0
	}
	cache, _ := perTokenToUSDPerM(p.InputCacheRead)
	return Price{PromptUSD: prompt, CompletionUSD: completion, CacheReadUSD: cache, Known: true}
}

// perTokenToUSDPerM parses a per-token decimal string into dollars per one
// million tokens. An empty or unparseable string reports not-ok so the caller
// can keep the price unknown instead of defaulting to zero.
func perTokenToUSDPerM(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v * 1_000_000, true
}

// Endpoint is one upstream provider serving a model through OpenRouter.
//
// OpenRouter's catalog presents a model as a single id, but that id is a
// routing decision across many real providers whose prices differ by up to 5x
// for byte-identical weights (verified live: deepseek-v4-flash is served by 15
// providers from $0.04/M to $0.21/M input). The picker's whole reason for
// showing costs is that spread, so the breakdown is a first-class type rather
// than a formatted string.
type Endpoint struct {
	Provider      string // upstream's display name, e.g. "DeepInfra"
	Quantization  string // "fp8", "fp4", "unknown" (as reported), "" when absent
	ContextLength int    // this endpoint's window, which can differ from the model's
	MaxCompletion int    // max output tokens (0 = unreported)
	Price         Price
	Uptime30m     float64 // percent over the last 30m; negative = unreported
}

// endpointsPayload is the /models/{id}/endpoints response.
type endpointsPayload struct {
	Data struct {
		ID        string `json:"id"`
		Endpoints []struct {
			ProviderName  string      `json:"provider_name"`
			ContextLength int         `json:"context_length"`
			Quantization  string      `json:"quantization"`
			MaxCompletion int         `json:"max_completion_tokens"`
			Pricing       *apiPricing `json:"pricing"`
			Uptime30m     *float64    `json:"uptime_last_30m"`
		} `json:"endpoints"`
	} `json:"data"`
}

// BaseModelID strips an OpenRouter variant suffix (":free", ":nitro",
// ":floor") from a model id. The endpoints route is keyed by the base model —
// GET /models/vendor/name:free/endpoints is a 404 — while the picker carries
// the variant id because that is what a request must name.
func BaseModelID(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[:i]
	}
	return id
}

// ListOpenRouterEndpoints returns every upstream provider serving modelID,
// cheapest input rate first. The API key is optional — the endpoints route
// answers unauthenticated — but is sent when present so the response reflects
// any account-level provider preferences.
func ListOpenRouterEndpoints(httpClient *http.Client, apiKey, modelID string) ([]Endpoint, error) {
	return listOpenRouterEndpointsAt(OpenRouterBase, httpClient, apiKey, modelID)
}

// listOpenRouterEndpointsAt is ListOpenRouterEndpoints against an injectable
// base URL — the same test seam as listOpenRouterRankedAt.
func listOpenRouterEndpointsAt(base string, httpClient *http.Client, apiKey, modelID string) ([]Endpoint, error) {
	id := BaseModelID(strings.TrimSpace(modelID))
	if id == "" {
		return nil, fmt.Errorf("openrouter endpoints: empty model id")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, base+"/models/"+id+"/endpoints", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter endpoints: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s/models/%s/endpoints: %s: %s", base, id, resp.Status, strings.TrimSpace(string(body)))
	}
	var payload endpointsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse endpoints: %w", err)
	}
	out := make([]Endpoint, 0, len(payload.Data.Endpoints))
	for _, e := range payload.Data.Endpoints {
		ep := Endpoint{
			Provider:      strings.TrimSpace(e.ProviderName),
			ContextLength: e.ContextLength,
			MaxCompletion: e.MaxCompletion,
			Price:         e.Pricing.price(),
			Uptime30m:     -1,
		}
		// "unknown" is the API's own placeholder for an undisclosed
		// quantization. Passing it through would put the word "unknown" in a
		// column where every other row names a real format; an empty string
		// lets the renderer omit the field instead.
		if q := strings.TrimSpace(e.Quantization); q != "" && q != "unknown" {
			ep.Quantization = q
		}
		if e.Uptime30m != nil {
			ep.Uptime30m = *e.Uptime30m
		}
		if ep.Provider == "" {
			continue
		}
		out = append(out, ep)
	}
	SortEndpoints(out)
	return out, nil
}

// SortEndpoints orders endpoints by what the user is choosing between:
// cheapest input rate first, then cheapest output, then name. Endpoints with
// no published price sort last — an unpriced row must never head a list that
// is read as "cheapest first".
func SortEndpoints(eps []Endpoint) {
	sort.SliceStable(eps, func(i, j int) bool {
		a, b := eps[i], eps[j]
		if a.Price.Known != b.Price.Known {
			return a.Price.Known
		}
		if a.Price.Known && a.Price.PromptUSD != b.Price.PromptUSD {
			return a.Price.PromptUSD < b.Price.PromptUSD
		}
		if a.Price.Known && a.Price.CompletionUSD != b.Price.CompletionUSD {
			return a.Price.CompletionUSD < b.Price.CompletionUSD
		}
		return a.Provider < b.Provider
	})
}

// PriceSpread reports how many times more expensive the dearest priced
// endpoint is than the cheapest, and whether the figure means anything. It is
// the one number that justifies the whole breakdown: a 1.1x spread is noise,
// a 5x spread is the difference between a cheap session and an expensive one.
// Free endpoints ($0 input) yield no spread — the ratio would be infinite.
func PriceSpread(eps []Endpoint) (float64, bool) {
	lo, hi := 0.0, 0.0
	for _, e := range eps {
		if !e.Price.Known || e.Price.PromptUSD <= 0 {
			continue
		}
		if lo == 0 || e.Price.PromptUSD < lo {
			lo = e.Price.PromptUSD
		}
		if e.Price.PromptUSD > hi {
			hi = e.Price.PromptUSD
		}
	}
	if lo == 0 || hi == 0 || hi <= lo {
		return 0, false
	}
	return hi / lo, true
}
