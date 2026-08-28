package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/proxy"
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

func ptrF(v float64) *float64 { return &v }
