package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/raketenkater/ultra-zen/internal/models"
)

// UsagePollInterval is how often the usage poller re-fetches provider usage.
const UsagePollInterval = 60 * time.Second

// StartUsagePoller launches a background goroutine that every UsagePollInterval
// fetches live usage for every provider in cfg.Upstreams, mirroring
// models.StartRecheckPoller (first pass immediate). It never blocks the request
// path: fetch errors keep the last good row and are recorded in Detail. Stops
// when ctx is cancelled (session teardown). httpClient should be the session's
// shared client.
func (s *Server) StartUsagePoller(ctx context.Context, httpClient *http.Client) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	go func() {
		ticker := time.NewTicker(UsagePollInterval)
		defer ticker.Stop()
		s.pollUsageOnce(httpClient)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.pollUsageOnce(httpClient)
			}
		}
	}()
}

// pollUsageOnce fetches usage for every unique provider in the upstream set.
func (s *Server) pollUsageOnce(httpClient *http.Client) {
	seen := map[string]bool{}
	keys := map[string]string{}
	for _, u := range s.cfg.Upstreams {
		if u.Provider == "" || seen[u.Provider] {
			continue
		}
		seen[u.Provider] = true
		keys[u.Provider] = u.APIKey
	}
	for provider, key := range keys {
		if key == "" {
			continue // skip providers with no key
		}
		s.fetchProviderUsage(httpClient, provider, key)
	}
}

// fetchProviderUsage fetches and stores one provider's live usage, tolerant of
// partial/empty payloads and transport errors (last good row is kept).
func (s *Server) fetchProviderUsage(httpClient *http.Client, provider, key string) {
	base := models.BaseForProvider(provider)
	switch provider {
	case "openrouter":
		s.fetchOpenRouterUsage(httpClient, key)
	case "opencode-go":
		if strings.Contains(strings.ToLower(base), "opencode.ai/") {
			s.fetchZenUsage(httpClient, base, key)
		}
	case "groq", "saia":
		// Seeded from response headers in forwardWithRateLimit; no background
		// fetch. Leave kind=requests if a header was already seen.
		if row := s.usage.getRowSnapshot(provider); row == nil {
			s.usage.setRow(provider, &ProviderUsage{Name: provider, Kind: UsageRequests, Window: WindowMinute})
		}
	case "cerebras", "cohere", "modelscope", "huggingface", "codex":
		s.usage.setRow(provider, &ProviderUsage{
			Name:    provider,
			Kind:    UsageUnknown,
			Window:  WindowNone,
			Detail:  "no live usage endpoint; counting requests",
		})
	default:
		// Unknown BYO provider: count requests only.
		s.usage.setRow(provider, &ProviderUsage{Name: provider, Kind: UsageUnknown, Window: WindowNone})
	}
}

// fetchOpenRouterUsage GETs {OpenRouterBase}/key for the per-key cap and the
// free-tier window, then {OpenRouterBase}/credits for the account balance and
// the lifetime-purchased amount that sets the :free daily request cap.
// (/activity would report daily free_used but needs a management key.)
func (s *Server) fetchOpenRouterUsage(httpClient *http.Client, key string) {
	s.fetchOpenRouterUsageAt(models.OpenRouterBase, httpClient, key)
}

// fetchOpenRouterUsageAt is fetchOpenRouterUsage with an injectable base URL
// for tests — mirroring fetchOpenRouterUsageAt in internal/tui, which lets the
// parity test point both usage paths at the same fake server.
func (s *Server) fetchOpenRouterUsageAt(base string, httpClient *http.Client, key string) {
	url := base + "/key"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		s.usage.setRow("openrouter", providerOrKeep(s, "openrouter", "live fetch failed: "+err.Error()))
		return
	}
	defer resp.Body.Close()
	var payload struct {
		Data struct {
			LimitRemaining *float64 `json:"limit_remaining"`
			UsageDaily     *float64 `json:"usage_daily"`
			IsFreeTier     *bool    `json:"is_free_tier"`
			Limit          *float64 `json:"limit"`
			LimitReset     *string  `json:"limit_reset"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		row := s.usage.getRowSnapshot("openrouter")
		if row != nil {
			row.Detail = "parse error: " + err.Error()
			s.usage.setRow("openrouter", row)
		}
		return
	}
	if resp.StatusCode != http.StatusOK && payload.Error != nil {
		row := s.usage.getRowSnapshot("openrouter")
		if row != nil {
			row.Detail = payload.Error.Message
			s.usage.setRow("openrouter", row)
		}
		return
	}
	row := &ProviderUsage{Name: "openrouter", Kind: UsageCredits, Window: WindowDaily}
	if payload.Data.LimitRemaining != nil {
		rem := *payload.Data.LimitRemaining
		row.Remaining = &rem
	}
	if payload.Data.UsageDaily != nil {
		used := *payload.Data.UsageDaily
		row.Used = &used
	}
	if payload.Data.IsFreeTier != nil && *payload.Data.IsFreeTier {
		// Free tier resets daily: `limit` is the daily cap and `limit_reset` the
		// next daily reset time.
		if payload.Data.Limit != nil {
			capv := *payload.Data.Limit
			row.FreeLimit = &capv
		}
		if payload.Data.LimitReset != nil && *payload.Data.LimitReset != "" {
			row.Daily = &WindowStat{Status: "daily", ResetsAt: *payload.Data.LimitReset}
		}
	}
	// Account credits come from /credits: `limit_remaining` on /key is the
	// per-key cap, not the balance. Purchased lifetime also sets the free-tier
	// daily request cap (50 → 1000 at $10+). Same shared fetch and fold as the
	// launch banner (internal/tui) so the two views can never disagree.
	if total, used, ok := models.FetchOpenRouterCredits(httpClient, base, key); ok {
		applyOpenRouterCredits(row, total, used)
	}
	s.usage.setRow("openrouter", row)
}

// applyOpenRouterCredits folds /credits totals plus the local :free request
// tally into a fresh openrouter row. Shared by the poller and the launch-time
// banner fetch (internal/tui calls through the same logic via the exported
// proxy helper below) so both paths render identical numbers from identical
// upstream data.
func applyOpenRouterCredits(row *ProviderUsage, total, used float64) {
	balance := total - used
	row.Limit = &total
	row.Remaining = &balance
	row.Credits = &balance
	capN := models.OpenRouterFreeDailyCap(total)
	usedN := models.ORFreeRequests()
	row.FreeReqsUsed = &usedN
	row.FreeReqsLimit = &capN
	if usedN >= capN {
		row.Exhausted = true
	}
}

// ApplyOpenRouterCredits is the exported form of applyOpenRouterCredits for
// the launch-time banner in internal/tui, which builds the same canonical
// ProviderUsage row before any proxy exists.
func ApplyOpenRouterCredits(row *ProviderUsage, total, used float64) {
	applyOpenRouterCredits(row, total, used)
}

// fetchZenUsage GETs {base}/usage (opencode.ai zen gateway). It parses the
// rolling/weekly/monthly windows each carrying a percent and an ISO resetsAt.
func (s *Server) fetchZenUsage(httpClient *http.Client, base, key string) {
	url := strings.TrimRight(base, "/") + "/usage"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		s.usage.setRow("opencode-go", providerOrKeep(s, "opencode-go", "live fetch failed: "+err.Error()))
		return
	}
	defer resp.Body.Close()
	// The gateway wraps the windows in a "usage" envelope; older shapes put
	// them at the top level. Accept both: prefer the envelope when present.
	var payload struct {
		Usage *struct {
			Rolling *windowPayload `json:"rolling"`
			Weekly  *windowPayload `json:"weekly"`
			Monthly *windowPayload `json:"monthly"`
		} `json:"usage"`
		Rolling *windowPayload `json:"rolling"`
		Weekly  *windowPayload `json:"weekly"`
		Monthly *windowPayload `json:"monthly"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		row := s.usage.getRowSnapshot("opencode-go")
		if row != nil {
			row.Detail = "parse error: " + err.Error()
			s.usage.setRow("opencode-go", row)
		}
		return
	}
	if resp.StatusCode != http.StatusOK {
		if row := s.usage.getRowSnapshot("opencode-go"); row != nil {
			s.usage.setRow("opencode-go", row)
		}
		return
	}
	rolling, weekly, monthly := payload.Rolling, payload.Weekly, payload.Monthly
	if payload.Usage != nil {
		rolling, weekly, monthly = payload.Usage.Rolling, payload.Usage.Weekly, payload.Usage.Monthly
	}
	row := &ProviderUsage{Name: "opencode-go", Kind: UsageCredits, Window: Window5h}
	if rolling != nil {
		row.Rolling = rolling.toStat("rolling")
	}
	if weekly != nil {
		row.Weekly = weekly.toStat("weekly")
	}
	if monthly != nil {
		row.Monthly = monthly.toStat("monthly")
	}
	s.usage.setRow("opencode-go", row)
}

// windowPayload is one opencode usage window: a percent and an ISO reset time.
type windowPayload struct {
	Percent  int    `json:"percent"`
	ResetsAt string `json:"resetsAt"`
}

func (w *windowPayload) toStat(status string) *WindowStat {
	return &WindowStat{Status: status, Percent: w.Percent, ResetsAt: w.ResetsAt}
}

// providerOrKeep returns the last good row (with Detail set) when present, else a
// fresh unknown row with the given detail. Used so a transient fetch failure
// keeps the previous snapshot visible rather than wiping it.
func providerOrKeep(s *Server, provider, detail string) *ProviderUsage {
	if row := s.usage.getRowSnapshot(provider); row != nil {
		row.Detail = detail
		return row
	}
	return &ProviderUsage{Name: provider, Kind: UsageUnknown, Window: WindowNone, Detail: detail}
}

// getRowSnapshot returns a copy of a provider's current row, or nil. It is used
// by the poller to preserve the last good values across a failed refresh.
func (t *usageTracker) getRowSnapshot(provider string) *ProviderUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()
	row, ok := t.rows[provider]
	if !ok {
		return nil
	}
	cp := *row
	return &cp
}
