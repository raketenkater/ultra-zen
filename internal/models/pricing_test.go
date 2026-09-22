package models

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// endpointsFixture mirrors a real OpenRouter /models/{id}/endpoints response,
// including the two traps the live payload sets: per-token prices as decimal
// *strings* (a float64 field silently fails to decode them) and the literal
// "unknown" quantization placeholder.
const endpointsFixture = `{"data":{"id":"vendor/model","endpoints":[
{"provider_name":"Pricey","context_length":200000,"quantization":"fp8","max_completion_tokens":8192,
 "pricing":{"prompt":"0.00000021","completion":"0.00000056"},"uptime_last_30m":97.2},
{"provider_name":"Cheap","context_length":1048576,"quantization":"fp8","max_completion_tokens":943718,
 "pricing":{"prompt":"0.00000004","completion":"0.0000005","input_cache_read":"0.000000014"},"uptime_last_30m":99.95},
{"provider_name":"Middle","context_length":131072,"quantization":"unknown",
 "pricing":{"prompt":"0.00000009","completion":"0.00000018"},"uptime_last_30m":null},
{"provider_name":"Unpriced","context_length":65536,"pricing":null}
]}}`

func endpointsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/models/vendor/model/endpoints"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// TestListOpenRouterEndpointsOrdersCheapestFirst is the core acceptance check
// for the cost breakdown: every upstream is returned, converted to $/M, and
// ordered cheapest input rate first so the top row is always the best price.
func TestListOpenRouterEndpointsOrdersCheapestFirst(t *testing.T) {
	srv := endpointsServer(t, endpointsFixture)
	defer srv.Close()

	eps, err := listOpenRouterEndpointsAt(srv.URL, srv.Client(), "", "vendor/model")
	if err != nil {
		t.Fatalf("endpoints: %v", err)
	}
	if len(eps) != 4 {
		t.Fatalf("got %d endpoints, want 4", len(eps))
	}
	want := []string{"Cheap", "Middle", "Pricey", "Unpriced"}
	for i, name := range want {
		if eps[i].Provider != name {
			t.Fatalf("order = %v, want %v", providerNames(eps), want)
		}
	}
	// Per-token strings must arrive as dollars per million, not per token.
	if got := eps[0].Price.PromptUSD; got != 0.04 {
		t.Errorf("cheapest prompt = %v $/M, want 0.04", got)
	}
	if got := eps[0].Price.CompletionUSD; got != 0.5 {
		t.Errorf("cheapest completion = %v $/M, want 0.5", got)
	}
	if got := eps[0].Price.CacheReadUSD; got != 0.014 {
		t.Errorf("cheapest cache read = %v $/M, want 0.014", got)
	}
}

// TestEndpointsNeverInventAPrice is the "no invented costs" criterion: an
// endpoint the API prices at nothing must report Known == false, so the
// renderer says "credits"/"—" instead of printing an authoritative $0.00.
func TestEndpointsNeverInventAPrice(t *testing.T) {
	srv := endpointsServer(t, endpointsFixture)
	defer srv.Close()

	eps, err := listOpenRouterEndpointsAt(srv.URL, srv.Client(), "", "vendor/model")
	if err != nil {
		t.Fatalf("endpoints: %v", err)
	}
	last := eps[len(eps)-1]
	if last.Provider != "Unpriced" {
		t.Fatalf("unpriced endpoint sorted to %d, want last", len(eps)-1)
	}
	if last.Price.Known {
		t.Fatal("endpoint with no pricing object reported a known price")
	}
	if last.Price.Free() {
		t.Fatal("unpriced endpoint reported itself as free")
	}
	if got := last.Price.Short("credits"); got != "credits" {
		t.Errorf("Short() = %q, want %q", got, "credits")
	}
}

// TestEndpointsDropUnknownQuantization keeps the API's own "unknown"
// placeholder out of a column where every other row names a real format.
func TestEndpointsDropUnknownQuantization(t *testing.T) {
	srv := endpointsServer(t, endpointsFixture)
	defer srv.Close()

	eps, _ := listOpenRouterEndpointsAt(srv.URL, srv.Client(), "", "vendor/model")
	for _, e := range eps {
		if e.Quantization == "unknown" {
			t.Fatalf("%s kept the literal \"unknown\" quantization", e.Provider)
		}
	}
	if eps[1].Provider != "Middle" || eps[1].Quantization != "" {
		t.Errorf("Middle quantization = %q, want empty", eps[1].Quantization)
	}
	if eps[0].Quantization != "fp8" {
		t.Errorf("Cheap quantization = %q, want fp8", eps[0].Quantization)
	}
}

// TestEndpointsUnreportedUptimeIsNegative distinguishes "0% uptime" (a dead
// provider) from "no uptime reported" (a null in the payload). Rendering the
// second as 0.0% would libel a working provider.
func TestEndpointsUnreportedUptimeIsNegative(t *testing.T) {
	srv := endpointsServer(t, endpointsFixture)
	defer srv.Close()

	eps, _ := listOpenRouterEndpointsAt(srv.URL, srv.Client(), "", "vendor/model")
	if eps[1].Uptime30m >= 0 {
		t.Errorf("null uptime = %v, want negative sentinel", eps[1].Uptime30m)
	}
	if eps[0].Uptime30m < 99 {
		t.Errorf("reported uptime = %v, want ~99.95", eps[0].Uptime30m)
	}
}

// TestBaseModelIDStripsVariant pins the routing detail that makes the whole
// breakdown work: the endpoints route is keyed by the base model, so a
// ":free" id must be stripped or the request 404s.
func TestBaseModelIDStripsVariant(t *testing.T) {
	cases := map[string]string{
		"vendor/model:free":  "vendor/model",
		"vendor/model:nitro": "vendor/model",
		"vendor/model":       "vendor/model",
		"glm-5.2":            "glm-5.2",
	}
	for in, want := range cases {
		if got := BaseModelID(in); got != want {
			t.Errorf("BaseModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPriceSpreadReportsRealSpread covers the one number that justifies
// showing the breakdown at all.
func TestPriceSpreadReportsRealSpread(t *testing.T) {
	eps := []Endpoint{
		{Provider: "a", Price: Price{PromptUSD: 0.04, Known: true}},
		{Provider: "b", Price: Price{PromptUSD: 0.21, Known: true}},
		{Provider: "c", Price: Price{Known: false}},
	}
	got, ok := PriceSpread(eps)
	if !ok {
		t.Fatal("PriceSpread reported no spread for a 5.25x range")
	}
	if diff := got - 5.25; diff > 0.001 || diff < -0.001 {
		t.Errorf("spread = %v, want 5.25", got)
	}
	if _, ok := PriceSpread([]Endpoint{{Price: Price{Known: true}}}); ok {
		t.Error("a single free endpoint reported a price spread")
	}
}

// TestPriceShortPrecision checks the picker column stays informative across
// the four orders of magnitude real rates span. Three decimals below $1 is not
// cosmetic: the live DeepSeek V4 Flash endpoints price at 0.088, 0.09, 0.091,
// 0.0966 and 0.098 $/M, which two decimals collapses into two buckets and so
// destroys the ordering the column exists to show.
func TestPriceShortPrecision(t *testing.T) {
	cases := []struct {
		usd  float64
		want string
	}{
		{0, "free"},
		{0.04, "$0.040/M"},
		{0.009, "$0.009/M"},
		{0.6, "$0.600/M"},
		{2.5, "$2.50/M"},
		{15, "$15.0/M"},
	}
	for _, c := range cases {
		got := Price{PromptUSD: c.usd, Known: true}.Short("credits")
		if got != c.want {
			t.Errorf("Short(%v) = %q, want %q", c.usd, got, c.want)
		}
	}
}

func providerNames(eps []Endpoint) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Provider
	}
	return out
}
