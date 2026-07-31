package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMaxTokensClamp verifies the proxy clamps oversized max_tokens before
// forwarding, so the gateway never sees a value it would reject with 400.
func TestMaxTokensClamp(t *testing.T) {
	var gotMax int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotMax = req.MaxTokens
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	srv := New(Config{BaseURL: up.URL, APIKey: "k", Model: "m", Port: 0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"m","max_tokens":512000,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if gotMax != maxOutputTokens {
		t.Fatalf("max_tokens not clamped: got %d want %d", gotMax, maxOutputTokens)
	}
}

// TestRetryOn400Transient verifies that a transient 400 "Upstream request
// failed" (param:null) is retried with the SAME params and succeeds on the
// second call — the common case for a flaky upstream backend.
func TestRetryOn400Transient(t *testing.T) {
	var calls int
	var gotMax int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		calls++
		gotMax = req.MaxTokens
		if calls == 1 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"Error from provider (Console Go): Upstream request failed","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`))
			return
		}
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	srv := New(Config{BaseURL: up.URL, APIKey: "k", Model: "m", Port: 0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"m","max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("expected 2 upstream calls (initial + same-params retry), got %d", calls)
	}
	// Same-params retry: max_tokens should still be the clamped value (65536),
	// NOT halved, because the first retry succeeded.
	if gotMax != maxOutputTokens {
		t.Fatalf("same-params retry should keep max_tokens clamped: got %d want %d", gotMax, maxOutputTokens)
	}
}

// TestRetryOn400Halve verifies that when the same-params retry ALSO fails, the
// proxy falls back to halving max_tokens and retries a third time.
func TestRetryOn400Halve(t *testing.T) {
	var calls int
	var gotMax int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		calls++
		gotMax = req.MaxTokens
		if calls <= 2 {
			// First two calls fail (transient retry also fails).
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"Error from provider (Console Go): Upstream request failed","type":"invalid_request_error","param":null}}`))
			return
		}
		// Third call (halved max_tokens) succeeds.
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	srv := New(Config{BaseURL: up.URL, APIKey: "k", Model: "m", Port: 0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"m","max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 after halved retry, got %d", resp.StatusCode)
	}
	if calls != 3 {
		t.Fatalf("expected 3 upstream calls (initial + same + halved), got %d", calls)
	}
	// Third call should have halved max_tokens: 65536 / 2 = 32768.
	if gotMax != 32768 {
		t.Fatalf("halved retry should have max_tokens=32768: got %d", gotMax)
	}
}

// TestFreePoolRotatesAndStays verifies the failure seen in real Zen logs:
// FreeUsageLimitError retires that provider for this Claude Code session,
// retries the interrupted request on the selected OpenRouter fallback, and
// starts the next request there instead of hitting exhausted Zen again.
func TestFreePoolRotatesAndStays(t *testing.T) {
	var primaryCalls, fallbackCalls int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"type":"error","error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded"}}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "openrouter/free" {
			t.Errorf("fallback model = %q, want openrouter/free", req.Model)
		}
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer fallback.Close()

	srv := New(Config{
		BaseURL: primary.URL,
		APIKey:  "zen-key",
		Model:   "laguna-s-2.1-free",
		Fallbacks: []Upstream{
			{
				// This sibling shares the exhausted Zen provider and must be
				// skipped when FreeUsageLimitError opens that provider circuit.
				BaseURL: primary.URL,
				APIKey:  "zen-key",
				Model:   "another-zen-free",
			},
			{
				BaseURL: fallback.URL,
				APIKey:  "or-key",
				Model:   "openrouter/free",
			},
		},
		Port: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, resp.StatusCode)
		}
	}
	if primaryCalls != 1 {
		t.Fatalf("exhausted primary called %d times, want 1", primaryCalls)
	}
	if fallbackCalls != 2 {
		t.Fatalf("fallback called %d times, want 2", fallbackCalls)
	}
}

// TestTemporary429Retries verifies a provider throttle is retried rather than
// returned to Claude Code (which would terminate a background subagent).
func TestTemporary429Retries(t *testing.T) {
	var calls int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"type":"rate_limit_error","code":"provider_rate_limit_exceeded"}}`))
			return
		}
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	srv := New(Config{
		BaseURL:          up.URL,
		APIKey:           "k",
		Model:            "m",
		Port:             0,
		RateLimitRetries: 1,
		RateLimitBackoff: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 after retry", resp.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}

// TestOpenRouterDailyLimitStopsPool verifies an account-wide daily quota error
// does not burn another failed attempt on each selected OpenRouter model.
func TestOpenRouterDailyLimitStopsPool(t *testing.T) {
	var primaryCalls, fallbackCalls int
	openRouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model == "vendor/a:free" {
			primaryCalls++
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"Rate limit exceeded: free-models-per-day. Add 10 credits to unlock 1000 free model requests per day","code":429}}`))
			return
		}
		fallbackCalls++
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"should not run"},"finish_reason":"stop"}]}`))
	}))
	defer openRouter.Close()

	srv := New(Config{
		BaseURL: openRouter.URL,
		APIKey:  "same-openrouter-key",
		Model:   "vendor/a:free",
		Fallbacks: []Upstream{{
			BaseURL: openRouter.URL,
			APIKey:  "same-openrouter-key",
			Model:   "vendor/b:free",
		}},
		Port: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("request %d: status %d, want 429", i+1, resp.StatusCode)
		}
	}
	if primaryCalls != 1 {
		t.Fatalf("daily-limit primary called %d times, want 1", primaryCalls)
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback called %d times after account-wide exhaustion, want 0", fallbackCalls)
	}
}

// TestDailyLimitSwitchesProvider verifies the intended provider boundary: an
// OpenRouter daily-limit response skips every sibling OpenRouter route and
// replays the request on Zen, which remains the active provider afterward.
func TestDailyLimitSwitchesProvider(t *testing.T) {
	var openRouterCalls, zenCalls int
	openRouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openRouterCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit exceeded: free-models-per-day. Add 10 credits to unlock 1000 free model requests per day","code":429}}`))
	}))
	defer openRouter.Close()
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zenCalls++
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer zen.Close()

	srv := New(Config{
		BaseURL: openRouter.URL,
		APIKey:  "or-key",
		Model:   "vendor/a:free",
		Fallbacks: []Upstream{
			{BaseURL: openRouter.URL, APIKey: "or-key", Model: "vendor/b:free"},
			{BaseURL: zen.URL, APIKey: "zen-key", Model: "zen-model-free"},
		},
		Port: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i+1, resp.StatusCode)
		}
	}
	if openRouterCalls != 1 {
		t.Fatalf("OpenRouter called %d times, want 1", openRouterCalls)
	}
	if zenCalls != 2 {
		t.Fatalf("Zen called %d times, want 2", zenCalls)
	}
}

// TestRepairUnresolvedToolCalls verifies that an assistant message ending in
// tool_calls gets a stub tool result inserted, so the gateway (which 400s a
// dangling tool_calls turn) sees a complete round-trip.
func TestRepairUnresolvedToolCalls(t *testing.T) {
	assistant := openAIMessage{
		Role: "assistant",
		ToolCalls: []openAITool{
			{ID: "t1", Type: "function", Function: openAIToolFunc{Name: "Bash", Arguments: "{}"}},
		},
	}
	in := []openAIMessage{{Role: "user", Content: "hi"}, assistant}
	out := repairUnresolvedToolCalls(in)

	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(out), out)
	}
	if out[1].Role != "assistant" || len(out[1].ToolCalls) != 1 {
		t.Fatalf("assistant message mangled: %+v", out[1])
	}
	if out[2].Role != "tool" || out[2].ToolCallID != "t1" {
		t.Fatalf("expected stub tool result for t1, got %+v", out[2])
	}
}

// TestRepairKeepsResolved verifies resolved tool_calls are left alone.
func TestRepairKeepsResolved(t *testing.T) {
	in := []openAIMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []openAITool{{ID: "t1", Type: "function", Function: openAIToolFunc{Name: "Bash", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "t1", Content: "ok"},
	}
	out := repairUnresolvedToolCalls(in)
	if len(out) != 3 {
		t.Fatalf("expected no change, got %d messages: %+v", len(out), out)
	}
}
