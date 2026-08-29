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
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/auth"
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

// fetchOpenRouterUsage queries GET /api/v1/key with the normal API key, then
// GET /api/v1/credits for the account balance. /key's limit_remaining is only
// the per-key cap, so the balance and the :free request tally come from
// /credits plus the local counter — the exact same shared fetch/fold the
// in-session poller performs (models.FetchOpenRouterCredits +
// proxy.ApplyOpenRouterCredits), so both views render identical numbers.
func fetchOpenRouterUsage(client *http.Client, key string) usageSnapshot {
	return fetchOpenRouterUsageAt(models.OpenRouterBase, client, key)
}

func fetchOpenRouterUsageAt(base string, client *http.Client, key string) usageSnapshot {
	req, err := http.NewRequest(http.MethodGet, base+"/key", nil)
	if err != nil {
		return usageSnapshot{Provider: "openrouter", Line: "OpenRouter: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return usageSnapshot{Provider: "openrouter", Line: "OpenRouter: " + err.Error()}
	}
	defer resp.Body.Close()
	// A rejected key must not render as an empty (⇒ "unlimited") row.
	if resp.StatusCode != http.StatusOK {
		return usageSnapshot{Provider: "openrouter", Line: "OpenRouter: /key " + resp.Status}
	}
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
	row := &proxy.ProviderUsage{Name: "openrouter", Kind: proxy.UsageCredits, Window: proxy.WindowDaily}
	if payload.Data.LimitRemaining != nil {
		rem := *payload.Data.LimitRemaining
		row.Remaining = &rem
	}
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
	// Account credits come from /credits (/key's limit_remaining is the per-key
	// cap, not the balance); the :free request tally is the local persisted
	// counter. Identical fetch and fold as the in-session poller.
	if total, used, ok := models.FetchOpenRouterCredits(client, base, key); ok {
		proxy.ApplyOpenRouterCredits(row, total, used)
		return usageSnapshot{Provider: "openrouter", Usage: row, Ready: true}
	}
	// No /credits data: /key's fields alone. A null limit_remaining means the
	// key is unmetered; usagefmt renders that as "[OR unlimited]" so the
	// banner and the statusline show the identical token.
	return usageSnapshot{Provider: "openrouter", Usage: row, Ready: true}
}

// fetchZenGoUsage queries the opencode-go /usage endpoint for the
// rolling/weekly/monthly windows. Defensive: tolerates missing fields.
func fetchZenGoUsage(client *http.Client, key string) usageSnapshot {
	return fetchZenGoUsageAt(models.GoBase, client, key)
}

// fetchZenGoUsageAt is fetchZenGoUsage with an injectable base URL for tests.
func fetchZenGoUsageAt(base string, client *http.Client, key string) usageSnapshot {
	req, err := http.NewRequest(http.MethodGet, base+"/usage", nil)
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
	// The gateway wraps the windows in a "usage" envelope; older shapes put
	// them at the top level. Accept both: prefer the envelope when present.
	var payload struct {
		Usage *struct {
			Rolling *windowPayloadTUI `json:"rolling"`
			Weekly  *windowPayloadTUI `json:"weekly"`
			Monthly *windowPayloadTUI `json:"monthly"`
		} `json:"usage"`
		Rolling *windowPayloadTUI `json:"rolling"`
		Weekly  *windowPayloadTUI `json:"weekly"`
		Monthly *windowPayloadTUI `json:"monthly"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return usageSnapshot{Provider: "opencode-go", Line: "Zen: parse error"}
	}
	rolling, weekly, monthly := payload.Rolling, payload.Weekly, payload.Monthly
	if payload.Usage != nil {
		rolling, weekly, monthly = payload.Usage.Rolling, payload.Usage.Weekly, payload.Usage.Monthly
	}
	row := &proxy.ProviderUsage{Name: "opencode-go", Kind: proxy.UsageCredits, Window: proxy.Window5h}
	if rolling != nil {
		w := rolling
		row.Rolling = &proxy.WindowStat{Status: "rolling", Percent: w.Percent, ResetsAt: w.ResetsAt}
	}
	if weekly != nil {
		w := weekly
		row.Weekly = &proxy.WindowStat{Status: "weekly", Percent: w.Percent, ResetsAt: w.ResetsAt}
	}
	if monthly != nil {
		w := monthly
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
// the in-session statusline so the two views stay consistent. Rows render in
// the stable poolProviders order (map iteration would shuffle every launch).
// Providers without a live usage API show their Line ("live tracking once
// session starts") instead of a bare dash from the formatter.
func usageSummaryText(rows map[string]usageSnapshot) string {
	if len(rows) == 0 {
		return "no provider usage available"
	}
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		pi, pj := sliceIndex(poolProviders, names[i]), sliceIndex(poolProviders, names[j])
		if pi != pj {
			return pi < pj
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(rows))
	for _, name := range names {
		r := rows[name]
		if r.Usage == nil || (r.Usage.Kind == proxy.UsageUnknown && r.Usage.Detail == "") {
			if r.Line != "" {
				parts = append(parts, r.Line)
				continue
			}
		}
		parts = append(parts, usagefmt.FormatProviderUsage(*r.Usage))
	}
	return strings.Join(parts, " ")
}

// sliceIndex returns i if v == s[i] for some i, else -1.
func sliceIndex(s []string, v string) int {
	for i, e := range s {
		if e == v {
			return i
		}
	}
	return -1
}

// configuredProviderKeys returns the provider->API-key map for every provider
// ultra-zen can reach that currently has a key (env, flag, persistent store, or
// opencode's auth.json for the Zen credential). Only providers with a non-empty
// key are included, so the picker never tries a usage fetch it cannot
// authenticate. The primary provider is always considered; openrouter and
// opencode-go are added when keyed (they are the two providers with a live
// picker-time usage API).
func configuredProviderKeys(primary string) map[string]string {
	candidates := []string{primary, "openrouter", "opencode-go",
		"groq", "cerebras", "huggingface", "cohere", "modelscope", "saia"}
	out := map[string]string{}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		key := models.ProviderKey(p, "", "")
		if key == "" && p == "opencode-go" {
			// The Zen credential is usually NOT in ultra-zen's keystore: it lives
			// in opencode's auth.json (shared with `opencode auth login`), the
			// same place launch resolves it. Without this lookup the primary
			// provider's usage row silently vanished from the banner.
			if store, err := auth.Load(""); err == nil {
				if zenKey, err := auth.KeyFor(store, "opencode-go"); err == nil {
					key = zenKey
				}
			}
		}
		if key != "" {
			out[p] = key
		}
	}
	return out
}
