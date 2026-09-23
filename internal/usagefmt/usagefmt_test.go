package usagefmt

import (
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/proxy"
)

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }

// TestFormatOpenRouterCredits pins the headline rendering: account balance
// (Credits, from /credits) with the :free daily request tally as "used". The
// tally counts only requests routed through ultra-zen — a floor for usage —
// so the token must never flip it into "left" (cap minus a floored usage is
// an upper bound of what remains and would overstate it). The "~" reads as
// "at least".
func TestFormatOpenRouterCredits(t *testing.T) {
	u := proxy.ProviderUsage{
		Name: "openrouter", Kind: proxy.UsageCredits, Window: proxy.WindowDaily,
		Credits:       f(19.999408),
		FreeReqsUsed:  i(12),
		FreeReqsLimit: i(50),
	}
	got := FormatProviderUsage(u)
	want := "[OR $20.00 credits · ~12/50 free req used]"
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

// TestFormatOpenRouterFreeReqUsedExceedsCap: used>limit (possible after a
// manual counter bump or a cap tier change mid-day) renders as-is. The "used"
// direction has no negative-left artifact to clamp; the row-level Exhausted
// flip (applyOpenRouterCredits) is what gates the cap state.
func TestFormatOpenRouterFreeReqUsedExceedsCap(t *testing.T) {
	u := proxy.ProviderUsage{
		Name: "openrouter", Kind: proxy.UsageCredits,
		Credits: f(3), FreeReqsUsed: i(87), FreeReqsLimit: i(50),
	}
	if got := FormatProviderUsage(u); got != "[OR $3.00 credits · ~87/50 free req used]" {
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

// TestFormatExhausted: the drained marker wins over everything, including the
// credits headline (the counter flip at cap must show "drained", not a stale
// "~N/cap used" tally).
func TestFormatExhausted(t *testing.T) {
	u := proxy.ProviderUsage{
		Name: "openrouter", Kind: proxy.UsageCredits, Exhausted: true,
		Credits: f(20), FreeReqsUsed: i(1000), FreeReqsLimit: i(1000),
	}
	if got := FormatProviderUsage(u); got != "[OR drained]" {
		t.Fatalf("got %q", got)
	}
}

// TestFormatZenHealthMark pins the paid-plan signal: the Zen /usage endpoint
// exposes no money, and its only exhaustion signal is the gateway's per-window
// status ("rate-limited"). A healthy window renders exactly as before (no
// regression for "ok" or a missing status); an unhealthy one gets a single
// one-character "!" so the row stays short beside the [OR ...] token.
func TestFormatZenHealthMark(t *testing.T) {
	all := proxy.ProviderUsage{
		Name: "opencode-go", Kind: proxy.UsageCredits, Window: proxy.Window5h,
		Rolling: &proxy.WindowStat{Status: "rolling", Percent: 0, State: "ok"},
		Weekly:  &proxy.WindowStat{Status: "weekly", Percent: 99, State: "ok"},
		Monthly: &proxy.WindowStat{Status: "monthly", Percent: 100, State: "rate-limited"},
	}
	if got := FormatProviderUsage(all); got != "[Zen 5h 0% · wk 99% · mo 100%!]" {
		t.Fatalf("got %q", got)
	}
	healthy := proxy.ProviderUsage{
		Name: "opencode-go", Kind: proxy.UsageCredits,
		Rolling: &proxy.WindowStat{Status: "rolling", Percent: 0, State: "ok"},
		Weekly:  &proxy.WindowStat{Status: "weekly", Percent: 99}, // no State: legacy payloads
	}
	if got := FormatProviderUsage(healthy); got != "[Zen 5h 0% · wk 99%]" {
		t.Fatalf("got %q", got)
	}
}

// TestFormatZenMoneyPrecedence pins the forward-compatible money rendering:
// a Credits value (only ever set from a real "balance" field upstream) shows
// as "$N.NN left" beside the windows, or alone when the envelope shape gave
// no windows. Unknown money (nil) must never render as $0.00 — it is omitted.
func TestFormatZenMoneyPrecedence(t *testing.T) {
	withWindows := proxy.ProviderUsage{
		Name: "opencode-go", Kind: proxy.UsageCredits,
		Credits: f(4.10),
		Weekly:  &proxy.WindowStat{Status: "weekly", Percent: 99},
	}
	if got := FormatProviderUsage(withWindows); got != "[Zen $4.10 left · wk 99%]" {
		t.Fatalf("got %q", got)
	}
	windowsOnly := withWindows
	windowsOnly.Credits = nil
	if got := FormatProviderUsage(windowsOnly); got != "[Zen wk 99%]" {
		t.Fatalf("got %q", got)
	}
	noWindows := proxy.ProviderUsage{
		Name: "opencode-go", Kind: proxy.UsageCredits, Credits: f(7),
	}
	if got := FormatProviderUsage(noWindows); got != "[Zen $7.00 left]" {
		t.Fatalf("got %q", got)
	}
	unknown := proxy.ProviderUsage{Name: "opencode-go", Kind: proxy.UsageCredits}
	if got := FormatProviderUsage(unknown); got != "[opencode-go —]" {
		t.Fatalf("unknown money must stay a dash row, got %q", got)
	}
}

// TestFormatModelScopePoints pins the Magicube row. The "★" is load-bearing:
// the statusline puts this token next to dollar-denominated ones ("[OR $4.12
// credits]"), and a bare "[MS 94]" there reads as money. Points are a
// non-monetary integer allowance, so the unit marker must always be present.
func TestFormatModelScopePoints(t *testing.T) {
	got := FormatProviderUsage(proxy.ProviderUsage{
		Name: "modelscope", Kind: proxy.UsagePoints, Window: proxy.WindowNone,
		Points: i(94),
	})
	if got != "[MS 94★]" {
		t.Errorf("points row = %q, want \"[MS 94★]\"", got)
	}

	// Unlike every other row, this one has a number to show, so it must not
	// fall through to the ambiguous em-dash placeholder.
	if got := FormatProviderUsage(proxy.ProviderUsage{
		Name: "modelscope", Kind: proxy.UsagePoints, Detail: "no quota headers on this deployment",
		Points: i(250),
	}); got != "[MS 250★]" {
		t.Errorf("points row with Detail = %q, want \"[MS 250★]\"", got)
	}
}

// TestFormatModelScopePointsExhausted: a zero balance is a real reading, not a
// missing one — "0★" says "your daily grant is spent", which is actionable,
// where the generic drained wording ("drained") would hide the number and
// imply a hard failure.
func TestFormatModelScopePointsExhausted(t *testing.T) {
	got := FormatProviderUsage(proxy.ProviderUsage{
		Name: "modelscope", Kind: proxy.UsagePoints, Window: proxy.WindowNone,
		Points: i(0), Exhausted: true,
		Detail: "no Magicube points left; inference will be refused",
	})
	if got != "[MS 0★]" {
		t.Errorf("exhausted points row = %q, want \"[MS 0★]\"", got)
	}
}

// TestFormatPointsNeverRendersAsDollars guards the unit confusion the ★ exists
// to prevent: an integer allowance must never be formatted through the credits
// path, which would print "94" as a currency figure.
func TestFormatPointsNeverRendersAsDollars(t *testing.T) {
	got := FormatProviderUsage(proxy.ProviderUsage{
		Name: "modelscope", Kind: proxy.UsagePoints, Points: i(94),
	})
	if strings.Contains(got, "$") {
		t.Errorf("points row rendered a dollar sign: %q", got)
	}
}
