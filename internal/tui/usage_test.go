package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raketenkater/ultra-zen/internal/proxy"
	"github.com/raketenkater/ultra-zen/internal/usagefmt"
)

// TestUsageSummaryTextStableOrder pins the banner's provider order to
// poolProviders (map iteration would shuffle the rows between launches).
func TestUsageSummaryTextStableOrder(t *testing.T) {
	rows := map[string]usageSnapshot{
		"openrouter": {Provider: "openrouter", Usage: &proxy.ProviderUsage{Name: "openrouter", Kind: proxy.UsageCredits, Remaining: ptrF(1.0)}, Ready: true},
		"saia":       {Provider: "saia", Usage: &proxy.ProviderUsage{Name: "saia", Kind: proxy.UsageUnknown}, Line: "saia: live tracking once session starts", Ready: true},
		"modelscope": {Provider: "modelscope", Usage: &proxy.ProviderUsage{Name: "modelscope", Kind: proxy.UsageUnknown}, Line: "modelscope: live tracking once session starts", Ready: true},
		"opencode-go": {Provider: "opencode-go", Usage: &proxy.ProviderUsage{
			Name: "opencode-go", Kind: proxy.UsageCredits,
			Rolling: &proxy.WindowStat{Status: "rolling", Percent: 26},
		}, Ready: true},
	}
	got := usageSummaryText(rows)
	want := "[OR $1.000 left] [Zen 5h 26%] modelscope: live tracking once session starts saia: live tracking once session starts"
	if got != want {
		t.Fatalf("usageSummaryText = %q, want %q", got, want)
	}
}

// TestUsageSummaryTextUsesLineForUnknownProviders pins the fix for the bare
// "[modelscope —]" rows: providers without a live picker-time usage API show
// their explanatory Line instead of the formatter's empty dash.
func TestUsageSummaryTextUsesLineForUnknownProviders(t *testing.T) {
	rows := map[string]usageSnapshot{
		"modelscope": {Provider: "modelscope", Usage: &proxy.ProviderUsage{Name: "modelscope", Kind: proxy.UsageUnknown}, Line: "modelscope: live tracking once session starts", Ready: true},
	}
	got := usageSummaryText(rows)
	if strings.Contains(got, "—") {
		t.Fatalf("unknown provider rendered as a dash: %q", got)
	}
	if got != "modelscope: live tracking once session starts" {
		t.Fatalf("usageSummaryText = %q, want the Line text", got)
	}
}

// zenUsageRaw mirrors the verified live payload of GET
// https://opencode.ai/zen/go/v1/usage (probed 2026-08-29 with the account's
// real Zen key): no money fields, per-window percent + gateway health status
// + ISO resetsAt. The identical constant lives in
// internal/proxy/usage_poller_test.go; same bytes, same canonical row, same
// token on both views — these two tests pin that parity contract (same
// duplication discipline as the fakeOpenRouter pairs).
const zenUsageRaw = `{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-08-30T04:26:38.868Z"},"weekly":{"status":"ok","percent":99,"resetsAt":"2026-08-31T00:00:00.868Z"},"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-17T10:40:16.868Z"}}}`

// TestFetchZenGoUsageEnvelope pins the /usage response shape: the gateway wraps
// the windows in a "usage" envelope ({"usage":{"rolling":...}}); a parser that
// reads the top level only yields an empty [Zen —] row.
func TestFetchZenGoUsageEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"usage": map[string]any{
				"rolling": map[string]any{"status": "ok", "percent": 25, "resetsAt": "2026-08-28T13:47:50.369Z"},
				"weekly":  map[string]any{"status": "ok", "percent": 45, "resetsAt": "2026-08-31T00:00:00.369Z"},
				"monthly": map[string]any{"status": "ok", "percent": 72, "resetsAt": "2026-09-17T10:40:16.369Z"},
			},
		})
	}))
	defer srv.Close()

	row := fetchZenGoUsageAt(srv.URL, http.DefaultClient, "test-key")
	if row.Usage == nil {
		t.Fatal("no usage row parsed from envelope payload")
	}
	if row.Usage.Rolling == nil || row.Usage.Rolling.Percent != 25 {
		t.Fatalf("rolling = %+v, want percent 25", row.Usage.Rolling)
	}
	if row.Usage.Weekly == nil || row.Usage.Weekly.Percent != 45 {
		t.Fatalf("weekly = %+v, want percent 45", row.Usage.Weekly)
	}
	if row.Usage.Monthly == nil || row.Usage.Monthly.Percent != 72 {
		t.Fatalf("monthly = %+v, want percent 72", row.Usage.Monthly)
	}
}

// TestFetchZenGoUsageTopLevel keeps the legacy top-level shape working.
func TestFetchZenGoUsageTopLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"rolling": map[string]any{"status": "ok", "percent": 10, "resetsAt": "2026-08-28T13:47:50.369Z"},
		})
	}))
	defer srv.Close()

	row := fetchZenGoUsageAt(srv.URL, http.DefaultClient, "test-key")
	if row.Usage == nil || row.Usage.Rolling == nil || row.Usage.Rolling.Percent != 10 {
		t.Fatalf("top-level rolling not parsed: %+v", row.Usage)
	}
}

// fakeZen serves a fixed /usage body, mirroring the poller test's fake.
func fakeZen(body string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
}

// TestZenUsageBannerParityLivePayload pins the launch banner token built from
// the verified live payload: "[Zen 5h 0% · wk 99% · mo 100%!]" — the monthly
// window carries the gateway's "rate-limited" health and must render marked.
// The poller side (internal/proxy/usage_poller_test.go) pins the identical
// canonical row from the identical bytes, and both views render through
// usagefmt, so the golden here IS the statusline token. No dollars: the
// payload has none, and the formatter must not invent any.
func TestZenUsageBannerParityLivePayload(t *testing.T) {
	srv := fakeZen(zenUsageRaw, http.StatusOK)
	defer srv.Close()
	snap := fetchZenGoUsageAt(srv.URL, srv.Client(), "test-key")
	if snap.Usage == nil || !snap.Ready {
		t.Fatalf("live payload produced no row: %+v", snap)
	}
	want := "[Zen 5h 0% · wk 99% · mo 100%!]"
	got := usagefmt.FormatProviderUsage(*snap.Usage)
	if got != want {
		t.Fatalf("banner = %q, want %q", got, want)
	}
	if len([]rune(got)) > 40 {
		t.Fatalf("Zen token too wide for the statusline: %q (%d chars)", got, len([]rune(got)))
	}
}

// TestZenUsageBannerMissingFields pins the degraded path: an envelope with a
// single status-less window renders only that window's segment — no false
// 0% siblings, no "[Zen —]" regression, no money.
func TestZenUsageBannerMissingFields(t *testing.T) {
	srv := fakeZen(`{"usage":{"weekly":{"percent":99}}}`, http.StatusOK)
	defer srv.Close()
	snap := fetchZenGoUsageAt(srv.URL, srv.Client(), "test-key")
	if snap.Usage == nil || snap.Usage.Weekly == nil {
		t.Fatalf("missing-field payload produced no row: %+v", snap)
	}
	if got := usagefmt.FormatProviderUsage(*snap.Usage); got != "[Zen wk 99%]" {
		t.Fatalf("banner = %q, want %q", got, "[Zen wk 99%]")
	}
}

// TestZenUsageBannerZeroEdge pins the zero edge: fresh 0% windows are real
// values and render as such (an omitted window would wrongly claim data the
// plan never gave, but a present 0% must show 0%).
func TestZenUsageBannerZeroEdge(t *testing.T) {
	srv := fakeZen(`{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-09-01T00:00:00.000Z"},"weekly":{"status":"ok","percent":0,"resetsAt":"2026-09-07T00:00:00.000Z"},"monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-01T00:00:00.000Z"}}}`, http.StatusOK)
	defer srv.Close()
	snap := fetchZenGoUsageAt(srv.URL, srv.Client(), "test-key")
	if snap.Usage == nil {
		t.Fatalf("zero payload produced no row: %+v", snap)
	}
	if got := usagefmt.FormatProviderUsage(*snap.Usage); got != "[Zen 5h 0% · wk 0% · mo 0%]" {
		t.Fatalf("banner = %q", got)
	}
}

// TestZenUsageBannerRejectedKey pins the banner's rejected-key guard (it had
// none — a 401 body parsed as an empty window set and rendered "[Zen —]"):
// a non-200 must surface as an error line and the summary must not show the
// dash token.
func TestZenUsageBannerRejectedKey(t *testing.T) {
	srv := fakeZen(`{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
	defer srv.Close()
	snap := fetchZenGoUsageAt(srv.URL, srv.Client(), "bad-key")
	if snap.Usage != nil || snap.Ready {
		t.Fatalf("row = %+v ready=%v, want unusable snapshot", snap.Usage, snap.Ready)
	}
	if !strings.Contains(snap.Line, "401") {
		t.Fatalf("line = %q, want it to mention the status", snap.Line)
	}
	if summary := usageSummaryText(map[string]usageSnapshot{"opencode-go": snap}); strings.Contains(summary, "[Zen") {
		t.Fatalf("rejected key rendered a Zen token: %q", summary)
	}
}

// TestZenUsageBannerShapelessPayload pins the last defense: a 200 whose JSON
// is valid but carries none of the known shapes (the envelope change that
// once regressed the row to "[Zen —]") yields an error line, not an empty
// window row.
func TestZenUsageBannerShapelessPayload(t *testing.T) {
	srv := fakeZen(`{"data":{"windows":[]}}`, http.StatusOK)
	defer srv.Close()
	snap := fetchZenGoUsageAt(srv.URL, srv.Client(), "test-key")
	if snap.Usage != nil || snap.Ready {
		t.Fatalf("shapeless payload produced a row: %+v", snap)
	}
	if snap.Line == "" {
		t.Fatal("shapeless payload left no error line")
	}
}

// TestZenUsageBannerBalancePresentsMoney pins the forward-compatible money
// display: if opencode ever ships the console-balance proposal as a top-level
// "balance" sibling of "usage" (issue anomalyco/opencode#44189), the banner
// shows it beside the windows; until then the same parse yields no money.
func TestZenUsageBannerBalancePresentsMoney(t *testing.T) {
	withBal := zenUsageRaw[:len(zenUsageRaw)-1] + `,"balance":{"usd":4.10,"currency":"USD","asOf":"2026-08-29T00:00:00Z"}}`
	srv := fakeZen(withBal, http.StatusOK)
	defer srv.Close()
	snap := fetchZenGoUsageAt(srv.URL, srv.Client(), "test-key")
	if snap.Usage == nil || snap.Usage.Credits == nil || *snap.Usage.Credits != 4.10 {
		t.Fatalf("balance sibling not parsed: %+v", snap.Usage)
	}
	if got := usagefmt.FormatProviderUsage(*snap.Usage); got != "[Zen $4.10 left · 5h 0% · wk 99% · mo 100%!]" {
		t.Fatalf("banner = %q", got)
	}
}

func ptrF(v float64) *float64 { return &v }

// seedORFreeCount pins the local :free tally for today in an isolated cache
// dir so the parity goldens are deterministic.
func seedORFreeCount(t *testing.T, n int64) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	p := filepath.Join(dir, "ultra-zen", "openrouter-free-requests.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"day":   time.Now().UTC().Format("2006-01-02"),
		"count": n,
	})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeOpenRouter serves the same /key + /credits payloads as the proxy
// package's parity test (internal/proxy/usage_poller_test.go). Same fake,
// same seed → the banner and the in-session statusline MUST show identical
// tokens; these two tests pin that contract from both sides.
func fakeOpenRouter(keyLimit *float64, credits bool, total, used float64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/key":
			data := map[string]any{"is_free_tier": false, "usage_daily": 0}
			if keyLimit != nil {
				data["limit"] = *keyLimit
				data["limit_remaining"] = *keyLimit
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/credits":
			if !credits {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":{"code":403}}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"total_credits": total,
				"total_usage":   used,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestFetchOpenRouterUsageCreditsParity pins the launch banner against the
// same golden values the poller test asserts: $20.00 lifetime → balance
// 19.99, 1000/day cap, tally 7 → "~7/1000 free req used" (the token states
// counted usage; it never subtracts from the cap, which would overstate
// what remains).
func TestFetchOpenRouterUsageCreditsParity(t *testing.T) {
	seedORFreeCount(t, 7)
	lim := 1.0
	srv := fakeOpenRouter(&lim, true, 20.0, 0.01)
	defer srv.Close()
	snap := fetchOpenRouterUsageAt(srv.URL, srv.Client(), "sk-or-test")
	if snap.Usage == nil {
		t.Fatalf("no row: %+v", snap)
	}
	want := "[OR $19.99 credits · ~7/1000 free req used]"
	if got := usagefmt.FormatProviderUsage(*snap.Usage); got != want {
		t.Fatalf("banner = %q, want %q", got, want)
	}
	if snap.Usage.Exhausted {
		t.Fatal("must not be exhausted at 7/1000")
	}
}

// TestFetchOpenRouterUsageAtCap pins the shared exhaustion flip: tally 50
// against a 50/day cap (< $10 lifetime) renders "[openrouter hit]" exactly as
// the poller row does.
func TestFetchOpenRouterUsageAtCap(t *testing.T) {
	seedORFreeCount(t, 50)
	srv := fakeOpenRouter(nil, true, 5.0, 1.0)
	defer srv.Close()
	snap := fetchOpenRouterUsageAt(srv.URL, srv.Client(), "sk-or-test")
	if snap.Usage == nil || !snap.Usage.Exhausted {
		t.Fatalf("row = %+v, want Exhausted", snap.Usage)
	}
	if got := usagefmt.FormatProviderUsage(*snap.Usage); got != "[openrouter hit]" {
		t.Fatalf("banner = %q", got)
	}
}

// TestFetchOpenRouterUsageCreditsSkip: /credits 403 leaves only /key data —
// same "[OR $0.500 left]" the poller renders.
func TestFetchOpenRouterUsageCreditsSkip(t *testing.T) {
	seedORFreeCount(t, 3)
	lim := 0.5
	srv := fakeOpenRouter(&lim, false, 0, 0)
	defer srv.Close()
	snap := fetchOpenRouterUsageAt(srv.URL, srv.Client(), "sk-or-test")
	if snap.Usage == nil || snap.Usage.Credits != nil {
		t.Fatalf("row = %+v, want /key-only (no Credits)", snap.Usage)
	}
	if got := usagefmt.FormatProviderUsage(*snap.Usage); got != "[OR $0.500 left]" {
		t.Fatalf("banner = %q", got)
	}
}

// TestFetchOpenRouterUsageKeyRejected: a /key failure (bad/expired key) must
// surface as an error line, never as the empty row that would render
// "[OR unlimited]".
func TestFetchOpenRouterUsageKeyRejected(t *testing.T) {
	seedORFreeCount(t, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 401}})
	}))
	defer srv.Close()
	snap := fetchOpenRouterUsageAt(srv.URL, srv.Client(), "bad-key")
	if snap.Usage != nil || snap.Ready {
		t.Fatalf("row = %+v ready=%v, want unusable snapshot", snap.Usage, snap.Ready)
	}
	if !strings.Contains(snap.Line, "401") {
		t.Fatalf("line = %q, want it to mention the status", snap.Line)
	}
}

// TestFetchOpenRouterUsageKeyRejectedNoErrorField: the guard covers non-200
// bodies WITHOUT an error field too (a proxy/CDN 403 HTML-JSON hybrid, for
// instance) — they must still surface as the rejection Line, never as the
// empty row that renders "[OR unlimited]". The poller pins the same body
// shape on its side (internal/proxy/usage_poller_test.go).
func TestFetchOpenRouterUsageKeyRejectedNoErrorField(t *testing.T) {
	seedORFreeCount(t, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"label": nil}})
	}))
	defer srv.Close()
	snap := fetchOpenRouterUsageAt(srv.URL, srv.Client(), "bad-key")
	if snap.Usage != nil || snap.Ready {
		t.Fatalf("row = %+v ready=%v, want unusable snapshot", snap.Usage, snap.Ready)
	}
	if !strings.Contains(snap.Line, "403") {
		t.Fatalf("line = %q, want it to mention the status", snap.Line)
	}
	if strings.Contains(usageSummaryText(map[string]usageSnapshot{"openrouter": snap}), "unlimited") {
		t.Fatalf("rejected key rendered unlimited: %q", usageSummaryText(map[string]usageSnapshot{"openrouter": snap}))
	}
}

// TestFetchOpenRouterUsageUnlimited pins the parity fix for the old divergent
// "OpenRouter: unlimited credits" string: the canonical row now renders the
// identical "[OR unlimited]" token the statusline shows.
func TestFetchOpenRouterUsageUnlimited(t *testing.T) {
	seedORFreeCount(t, 0)
	srv := fakeOpenRouter(nil, false, 0, 0)
	defer srv.Close()
	snap := fetchOpenRouterUsageAt(srv.URL, srv.Client(), "sk-or-test")
	if snap.Usage == nil {
		t.Fatalf("expected canonical row, got Line %q", snap.Line)
	}
	if got := usagefmt.FormatProviderUsage(*snap.Usage); got != "[OR unlimited]" {
		t.Fatalf("banner = %q, want %q", got, "[OR unlimited]")
	}
}
