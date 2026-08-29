// Package usagefmt renders a uniform per-provider usage token shared by the
// in-session statusline (cmd/ultra-zen) and the launch-time TUI banner
// (internal/tui), so both show identical, correct per-provider summaries.
package usagefmt

import (
	"fmt"
	"strings"
	"time"

	"github.com/raketenkater/ultra-zen/internal/proxy"
)

// humanizeReset renders an ISO timestamp as a compact relative reset label.
// It returns "" when the timestamp cannot be parsed.
func humanizeReset(resetsAt string) string {
	if resetsAt == "" {
		return ""
	}
	ts, err := time.Parse(time.RFC3339, resetsAt)
	if err != nil {
		// Try the datetime without timezone (OpenRouter limit_reset is often
		// a bare ISO like "2026-08-26T00:00:00").
		ts, err = time.Parse("2006-01-02T15:04:05", resetsAt)
		if err != nil {
			return ""
		}
	}
	now := time.Now()
	diff := ts.Sub(now)
	if diff < 0 {
		return "reset"
	}
	switch {
	case diff < time.Minute:
		return "now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh", int(diff.Hours()))
	default:
		return ts.Local().Format("15:04")
	}
}

// humanizeResetSeconds renders a numeric rate-limit reset value. Header reset
// values are either a Unix epoch (very large) or seconds-until-reset (small);
// we disambiguate by magnitude, matching the parser's heuristic.
func humanizeResetSeconds(v int64) string {
	if v <= 0 {
		return ""
	}
	if v > 1e10 {
		return humanizeReset(time.Unix(v, 0).UTC().Format(time.RFC3339))
	}
	d := time.Duration(v) * time.Second
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// FormatProviderUsage renders one ProviderUsage into a single compact bracketed
// token. It is the single source of truth for both the statusline and the TUI
// banner.
func FormatProviderUsage(u proxy.ProviderUsage) string {
	title := u.Name
	if title == "" {
		title = "?"
	}
	if u.Exhausted {
		return fmt.Sprintf("[%s hit]", title)
	}
	switch u.Kind {
	case proxy.UsageCredits:
		switch title {
		case "openrouter":
			// Account credits (from /credits) are the headline number: the
			// balance, plus the :free request tally against today's cap. The
			// tally is "~"-marked because it counts only requests routed
			// through ultra-zen — a floor for "left", never more than what
			// OpenRouter itself meters.
			if u.Credits != nil {
				tok := fmt.Sprintf("[OR $%.2f credits", *u.Credits)
				if u.FreeReqsLimit != nil {
					left := *u.FreeReqsLimit
					if u.FreeReqsUsed != nil {
						left -= *u.FreeReqsUsed
						if left < 0 {
							left = 0
						}
					}
					tok += fmt.Sprintf(" · ~%d free req left", left)
				}
				return tok + "]"
			}
			if u.Remaining == nil && u.FreeLimit == nil {
				// /key reported a null limit_remaining (and no /credits
				// data): the key is unmetered. Render the fact instead of
				// falling through to the ambiguous "[openrouter —]" dash,
				// so the statusline and launch banner show the same token.
				return "[OR unlimited]"
			}
			reset := humanizeReset(resetOf(u.Daily))
			if u.FreeLimit != nil && *u.FreeLimit > 0 {
				// Free tier: daily cap + remaining balance + daily reset.
				if u.Remaining != nil {
					tok := fmt.Sprintf("[OR free $%.3f left", *u.Remaining)
					if reset != "" {
						tok += " · daily reset " + reset
					}
					return tok + "]"
				}
				return fmt.Sprintf("[OR free $%.3f cap]", *u.FreeLimit)
			}
			if u.Remaining != nil {
				if u.Limit != nil && *u.Limit > 0 {
					return fmt.Sprintf("[OR $%.3f left / $%.3f cap]", *u.Remaining, *u.Limit)
				}
				return fmt.Sprintf("[OR $%.3f left]", *u.Remaining)
			}
		case "opencode-go":
			// Zen: surface the rolling/weekly/monthly windows.
			var parts []string
			if w := u.Rolling; w != nil {
				parts = append(parts, fmt.Sprintf("5h %d%%", w.Percent))
			}
			if w := u.Weekly; w != nil {
				parts = append(parts, fmt.Sprintf("wk %d%%", w.Percent))
			}
			if w := u.Monthly; w != nil {
				parts = append(parts, fmt.Sprintf("mo %d%%", w.Percent))
			}
			if len(parts) > 0 {
				return fmt.Sprintf("[Zen %s]", strings.Join(parts, " · "))
			}
			if u.Remaining != nil {
				return fmt.Sprintf("[Zen $%.3f left]", *u.Remaining)
			}
		}
		// Generic credits provider.
		if u.Remaining != nil {
			return fmt.Sprintf("[%s $%.3f left]", title, *u.Remaining)
		}
	case proxy.UsageRequests:
		// Request-metered providers: remaining/limit + reset window.
		if u.RequestsLimit != nil && u.RequestsUsed != nil {
			rem := *u.RequestsLimit - *u.RequestsUsed
			reset := ""
			if u.RequestsReset != nil {
				reset = humanizeResetSeconds(*u.RequestsReset)
			}
			tok := fmt.Sprintf("[%s %d/%d", title, rem, *u.RequestsLimit)
			if reset != "" {
				tok += " · reset " + reset
			}
			return tok + "]"
		}
		if u.RequestsUsed != nil {
			return fmt.Sprintf("[%s %d req]", title, *u.RequestsUsed)
		}
		if u.Percent != nil {
			return fmt.Sprintf("[%s %d%%]", title, *u.Percent)
		}
	}
	if u.Detail != "" {
		return fmt.Sprintf("[%s —]", title)
	}
	return fmt.Sprintf("[%s —]", title)
}

// resetOf returns the ResetsAt of a window or "".
func resetOf(w *proxy.WindowStat) string {
	if w == nil {
		return ""
	}
	return w.ResetsAt
}
