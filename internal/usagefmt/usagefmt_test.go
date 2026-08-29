package usagefmt

import (
	"testing"

	"github.com/raketenkater/ultra-zen/internal/proxy"
)

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }

// TestFormatOpenRouterCredits pins the headline rendering: account balance
// (Credits, from /credits) with the :free daily request tally as "left". The
// tally is "~"-marked because the local counter only sees requests routed
// through ultra-zen — it must never read as an exact account-wide number.
func TestFormatOpenRouterCredits(t *testing.T) {
	u := proxy.ProviderUsage{
		Name: "openrouter", Kind: proxy.UsageCredits, Window: proxy.WindowDaily,
		Credits:       f(19.999408),
		FreeReqsUsed:  i(12),
		FreeReqsLimit: i(50),
	}
	got := FormatProviderUsage(u)
	want := "[OR $20.00 credits · ~38 free req left]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestFormatOpenRouterCreditsNoTally: /credits alone still renders.
func TestFormatOpenRouterCreditsNoTally(t *testing.T) {
	u := proxy.ProviderUsage{Name: "openrouter", Kind: proxy.UsageCredits, Credits: f(5)}
	if got := FormatProviderUsage(u); got != "[OR $5.00 credits]" {
		t.Fatalf("got %q", got)
	}
}

// TestFormatOpenRouterFreeReqLeftClamps: used>limit (possible after a manual
// API-key reset while the counter kept its day's tally) must clamp to 0, not
// render a negative "left".
func TestFormatOpenRouterFreeReqLeftClamps(t *testing.T) {
	u := proxy.ProviderUsage{
		Name: "openrouter", Kind: proxy.UsageCredits,
		Credits: f(3), FreeReqsUsed: i(87), FreeReqsLimit: i(50),
	}
	if got := FormatProviderUsage(u); got != "[OR $3.00 credits · ~0 free req left]" {
		t.Fatalf("got %q", got)
	}
}

// TestFormatOpenRouterUnlimited pins the parity fix: a null limit_remaining
// key with no /credits data renders "[OR unlimited]" from the canonical row —
// the in-session poller never had a fallback Line like the launch banner's old
// "OpenRouter: unlimited credits" string, so the two views diverged.
func TestFormatOpenRouterUnlimited(t *testing.T) {
	u := proxy.ProviderUsage{Name: "openrouter", Kind: proxy.UsageCredits, Window: proxy.WindowDaily}
	if got := FormatProviderUsage(u); got != "[OR unlimited]" {
		t.Fatalf("got %q", got)
	}
}

// TestFormatOpenRouterFreeTierLegacy keeps the old /key-only shape working
// when /credits is unreachable.
func TestFormatOpenRouterFreeTierLegacy(t *testing.T) {
	u := proxy.ProviderUsage{
		Name: "openrouter", Kind: proxy.UsageCredits, Window: proxy.WindowDaily,
		Remaining: f(0.5), FreeLimit: f(1.0),
	}
	got := FormatProviderUsage(u)
	if got != "[OR free $0.500 left]" {
		t.Fatalf("got %q", got)
	}
}

// TestFormatExhausted: the hit marker wins over everything, including the
// credits headline (the counter flip at cap must show "hit", not a stale
// "~N left").
func TestFormatExhausted(t *testing.T) {
	u := proxy.ProviderUsage{
		Name: "openrouter", Kind: proxy.UsageCredits, Exhausted: true,
		Credits: f(20), FreeReqsUsed: i(1000), FreeReqsLimit: i(1000),
	}
	if got := FormatProviderUsage(u); got != "[openrouter hit]" {
		t.Fatalf("got %q", got)
	}
}
