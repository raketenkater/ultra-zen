package proxy

import (
	"context"
	"encoding/json"
	"io"
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
			Name:   provider,
			Kind:   UsageUnknown,
			Window: WindowNone,
			Detail: "no live usage endpoint; counting requests",
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
	rawBody, readErr := io.ReadAll(resp.Body)
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
	if readErr == nil && len(rawBody) > 0 {
		_ = json.Unmarshal(rawBody, &payload)
	}
	if readErr != nil {
		row := s.usage.getRowSnapshot("openrouter")
		if row != nil {
			row.Detail = "parse error: " + readErr.Error()
			s.usage.setRow("openrouter", row)
		}
		return
	}
	// Any non-200 /key is a rejected (or otherwise unusable) key: never build
	// a row from the empty payload — an all-nil row renders "[OR unlimited]"
	// while the launch banner (internal/tui) renders the rejection line. The
	// body's error.message, when present, is the better detail; otherwise
	// record the status. Keep the last good row with that detail.
	if resp.StatusCode != http.StatusOK {
		// A 401 with a CreditsError / "Insufficient balance" body is the live
		// "drained" signal — the OpenRouter account credit wallet is empty
		// (balance is console-only, no money API). MarkExhaustedFromBody flips
		// the row's Exhausted flag and stores the upstream message so the
		// statusline reads "drained" instead of the HTTP status the user
		// already knows about.
		if resp.StatusCode == http.StatusUnauthorized && len(rawBody) > 0 {
			if s.usage.MarkExhaustedFromBody("openrouter", rawBody) {
				detail := "drained"
				if payload.Error != nil && payload.Error.Message != "" {
					detail = "drained: " + payload.Error.Message
				}
				s.usage.setRow("openrouter", providerOrKeep(s, "openrouter", detail))
				return
			}
		}
		detail := "OpenRouter: /key " + resp.Status
		if payload.Error != nil && payload.Error.Message != "" {
			detail = payload.Error.Message
		}
		s.usage.setRow("openrouter", providerOrKeep(s, "openrouter", detail))
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

// fetchZenUsage GETs {base}/usage (opencode.ai zen gateway) and stores the
// canonical opencode-go row built by BuildZenUsage. The endpoint exposes no
// money fields today — a dollar figure is not reachable with a plain key (the
// credit balance lives behind the web console; anomalyco/opencode#44189) — so
// the row shows quota windows plus the gateway's per-window health, and
// nothing else: unknown money is omitted, never rendered as "$0.00" (which
// would read as "spent nothing").
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.usage.setRow("opencode-go", providerOrKeep(s, "opencode-go", "live fetch failed: "+err.Error()))
		return
	}
	if resp.StatusCode != http.StatusOK {
		// A 401 with a CreditsError/insufficient-balance body is the live
		// "drained" signal (anomalyco/opencode#44189 — the /usage endpoint
		// returns the same envelope when the console credit wallet is empty).
		// MarkExhaustedFromBody flips the row's Exhausted flag and stores the
		// upstream's own message in Detail so the statusline reads "drained"
		// instead of the HTTP status the user already knows about.
		if s.usage.MarkExhaustedFromBody("opencode-go", body) {
			s.usage.setRow("opencode-go", providerOrKeep(s, "opencode-go", "drained: "+strings.ToLower(string(body))))
			return
		}
		s.usage.setRow("opencode-go", providerOrKeep(s, "opencode-go", "Zen: /usage "+resp.Status))
		return
	}
	row, ok := BuildZenUsage(body)
	if !ok {
		s.usage.setRow("opencode-go", providerOrKeep(s, "opencode-go", "parse error: unexpected /usage payload"))
		return
	}
	s.usage.setRow("opencode-go", row)
}

// BuildZenUsage decodes an opencode /usage response body into the canonical
// "opencode-go" ProviderUsage row, accepting both the current
// {"usage":{...}} envelope and the legacy top-level window shape. It is the
// single parse both usage paths share — the in-session poller here and the
// launch banner (internal/tui calls this same function) — so a future
// envelope change cannot fix one path and silently break the other (the
// prior "[Zen —]" regression). ok=false on garbage or on a well-formed
// payload carrying no known signal; the caller keeps its last good row.
func BuildZenUsage(body []byte) (row *ProviderUsage, ok bool) {
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
		// Balance is not in the live payload today: the credit wallet is
		// console-only (anomalyco/opencode#44189), whose requested shape is
		// exactly this {"balance":{"usd":...}} sibling of "usage". Absent
		// fields decode to nil at zero cost, so the money row lights up
		// automatically if opencode ever ships it.
		Balance *struct {
			USD *float64 `json:"usd"`
		} `json:"balance"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	rolling, weekly, monthly := payload.Rolling, payload.Weekly, payload.Monthly
	if payload.Usage != nil {
		rolling, weekly, monthly = payload.Usage.Rolling, payload.Usage.Weekly, payload.Usage.Monthly
	}
	row = &ProviderUsage{Name: "opencode-go", Kind: UsageCredits, Window: Window5h}
	if rolling != nil {
		row.Rolling = rolling.toStat("rolling")
	}
	if weekly != nil {
		row.Weekly = weekly.toStat("weekly")
	}
	if monthly != nil {
		row.Monthly = monthly.toStat("monthly")
	}
	if payload.Balance != nil && payload.Balance.USD != nil {
		usd := *payload.Balance.USD
		row.Credits = &usd
	}
	// A 200 that carries neither windows nor a balance is treated as unusable:
	// the live endpoint always reports the windows, so an object without them
	// means the envelope shape changed under us — exactly the change that
	// once regressed the row to "[Zen —]". Keep the last good row instead.
	if row.Rolling == nil && row.Weekly == nil && row.Monthly == nil && row.Credits == nil {
		return nil, false
	}
	return row, true
}

// windowPayload is one opencode usage window: a percent, the gateway's
// health string and an ISO reset time.
type windowPayload struct {
	Percent  int    `json:"percent"`
	Status   string `json:"status"`
	ResetsAt string `json:"resetsAt"`
}

func (w *windowPayload) toStat(status string) *WindowStat {
	return &WindowStat{Status: status, Percent: w.Percent, ResetsAt: w.ResetsAt, State: w.Status}
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
