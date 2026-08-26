// Package tui: launch-time per-provider usage summary for the model picker.
//
// The running proxy exposes live usage at GET /v1/usage (surfaced in the Claude
// Code statusline). The LAUNCH picker runs BEFORE the proxy starts, so it cannot
// query that endpoint. Instead it fetches the same upstream signals directly, on
// a background goroutine, so the user sees remaining credits per provider while
// choosing a model — complementing (not duplicating) the in-session statusline.
package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/proxy"
	"github.com/raketenkater/ultra-zen/internal/usagefmt"
)

// usageSnapshot is the launch-time usage summary for one provider. The picker
// runs before the proxy exists, so it fetches the same upstream signals
// directly and stores the canonical ProviderUsage row — the banner is then
// rendered by the shared usagefmt formatter, identical to the in-session
// statusline.
type usageSnapshot struct {
	Provider string
	Usage    *proxy.ProviderUsage // canonical row; may be nil on hard error
	Line     string               // human-readable one-liner (error/fallback text)
	Ready    bool                 // false while fetching / on error (Line then holds the error)
}

// usageLoaded is emitted when the background usage fetch completes, prompting
// the picker to re-render its usage row.
type usageLoaded struct{ rows map[string]usageSnapshot }

// fetchUsage fetches a usage summary for every configured provider and emits a
// single usageLoaded message. Non-blocking: it runs on its own goroutine and
// returns immediately. Providers with live APIs (OpenRouter, opencode-go) get
// real numbers; the rest report that usage is tracked live once the session
// starts (the proxy has no picker-time endpoint for them).
func fetchUsage(providers map[string]string) tea.Cmd {
	return func() tea.Msg {
		rows := fetchUsageSync(providers)
		return usageLoaded{rows: rows}
	}
}

// fetchUsageSync performs the actual upstream calls. Exported for testability.
func fetchUsageSync(providers map[string]string) map[string]usageSnapshot {
	client := &http.Client{Timeout: 4 * time.Second}
	rows := map[string]usageSnapshot{}
	for provider, key := range providers {
		if key == "" {
			continue
		}
		switch provider {
		case "openrouter":
			rows[provider] = fetchOpenRouterUsage(client, key)
		case "opencode-go":
			rows[provider] = fetchZenGoUsage(client, key)
		default:
			// No live picker-time usage API for BYO free tiers; the proxy tracks
			// them in-session via /v1/usage.
			rows[provider] = usageSnapshot{
				Provider: provider,
				Usage:    &proxy.ProviderUsage{Name: provider, Kind: proxy.UsageUnknown, Window: proxy.WindowNone},
				Line:     provider + ": live tracking once session starts",
				Ready:    true,
			}
		}
	}
	return rows
}

// fetchOpenRouterUsage queries GET /api/v1/key with the normal API key (NOT
// /credits, which 403s on non-management keys). Parses the remaining credit
// balance, the free-tier daily cap + reset, and today's usage into a canonical
// ProviderUsage row rendered by the shared usagefmt formatter.
func fetchOpenRouterUsage(client *http.Client, key string) usageSnapshot {
	req, err := http.NewRequest(http.MethodGet, models.OpenRouterBase+"/key", nil)
	if err != nil {
		return usageSnapshot{Provider: "openrouter", Line: "OpenRouter: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return usageSnapshot{Provider: "openrouter", Line: "OpenRouter: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Data struct {
			LimitRemaining *float64 `json:"limit_remaining"`
			UsageDaily     *float64 `json:"usage_daily"`
			IsFreeTier     bool     `json:"is_free_tier"`
			Limit          *float64 `json:"limit"`
			LimitReset     *string  `json:"limit_reset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return usageSnapshot{Provider: "openrouter", Line: "OpenRouter: parse error"}
	}
	if payload.Data.LimitRemaining == nil {
		return usageSnapshot{Provider: "openrouter", Line: "OpenRouter: unlimited credits", Ready: true}
	}
	row := &proxy.ProviderUsage{Name: "openrouter", Kind: proxy.UsageCredits, Window: proxy.WindowDaily}
	rem := *payload.Data.LimitRemaining
	row.Remaining = &rem
	if payload.Data.UsageDaily != nil {
		used := *payload.Data.UsageDaily
		row.Used = &used
	}
	if payload.Data.IsFreeTier {
		if payload.Data.Limit != nil {
			capv := *payload.Data.Limit
			row.FreeLimit = &capv
		}
		if payload.Data.LimitReset != nil && *payload.Data.LimitReset != "" {
			row.Daily = &proxy.WindowStat{Status: "daily", ResetsAt: *payload.Data.LimitReset}
		}
	}
	return usageSnapshot{Provider: "openrouter", Usage: row, Ready: true}
}

// fetchZenGoUsage queries the opencode-go /usage endpoint for the
// rolling/weekly/monthly windows. Defensive: tolerates missing fields.
func fetchZenGoUsage(client *http.Client, key string) usageSnapshot {
	req, err := http.NewRequest(http.MethodGet, models.GoBase+"/usage", nil)
	if err != nil {
		return usageSnapshot{Provider: "opencode-go", Line: "Zen: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return usageSnapshot{Provider: "opencode-go", Line: "Zen: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Rolling  *windowPayloadTUI `json:"rolling"`
		Weekly   *windowPayloadTUI `json:"weekly"`
		Monthly  *windowPayloadTUI `json:"monthly"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return usageSnapshot{Provider: "opencode-go", Line: "Zen: parse error"}
	}
	row := &proxy.ProviderUsage{Name: "opencode-go", Kind: proxy.UsageCredits, Window: proxy.Window5h}
	if payload.Rolling != nil {
		w := payload.Rolling
		row.Rolling = &proxy.WindowStat{Status: "rolling", Percent: w.Percent, ResetsAt: w.ResetsAt}
	}
	if payload.Weekly != nil {
		w := payload.Weekly
		row.Weekly = &proxy.WindowStat{Status: "weekly", Percent: w.Percent, ResetsAt: w.ResetsAt}
	}
	if payload.Monthly != nil {
		w := payload.Monthly
		row.Monthly = &proxy.WindowStat{Status: "monthly", Percent: w.Percent, ResetsAt: w.ResetsAt}
	}
	return usageSnapshot{Provider: "opencode-go", Usage: row, Ready: true}
}

// windowPayloadTUI mirrors the opencode-go /usage window shape ({percent,...}).
type windowPayloadTUI struct {
	Status   string `json:"status"`
	Percent  int    `json:"percent"`
	ResetsAt string `json:"resetsAt"`
}

// usageSummaryText joins every provider's canonical usage row into one display
// string for the picker's usage banner. It uses the SAME shared formatter as
// the in-session statusline so the two views stay consistent.
func usageSummaryText(rows map[string]usageSnapshot) string {
	if len(rows) == 0 {
		return "no provider usage available"
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Usage != nil {
			parts = append(parts, usagefmt.FormatProviderUsage(*r.Usage))
		} else {
			parts = append(parts, r.Line)
		}
	}
	return strings.Join(parts, " ")
}

// configuredProviderKeys returns the provider->API-key map for every provider
// ultra-zen can reach that currently has a key (env, flag, or persistent store).
// Only providers with a non-empty key are included, so the picker never tries a
// usage fetch it cannot authenticate. The primary provider is always considered;
// openrouter and opencode-go are added when keyed (they are the two providers
// with a live picker-time usage API).
func configuredProviderKeys(primary string) map[string]string {
	candidates := []string{primary, "openrouter", "opencode-go",
		"groq", "cerebras", "huggingface", "cohere", "modelscope", "saia"}
	out := map[string]string{}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		key := models.ProviderKey(p, "", "")
		if key != "" {
			out[p] = key
		}
	}
	return out
}
