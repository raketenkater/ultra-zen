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

func TestInsufficientQuotaSkipsProviderSiblings(t *testing.T) {
	var firstCalls, siblingCalls, otherCalls int
	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model == "first" {
			firstCalls++
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`))
			return
		}
		siblingCalls++
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"wrong"}}]}`))
	}))
	defer modelScope.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls++
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer other.Close()

	srv := New(Config{
		Provider: "modelscope",
		BaseURL:  modelScope.URL,
		APIKey:   "ms-key",
		Model:    "first",
		Fallbacks: []Upstream{
			{Provider: "modelscope", BaseURL: modelScope.URL, APIKey: "ms-key", Model: "sibling"},
			{Provider: "opencode-go", BaseURL: other.URL, APIKey: "zen-key", Model: "zen-free"},
		},
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || firstCalls != 1 || siblingCalls != 0 || otherCalls != 1 {
		t.Fatalf("status=%d calls first=%d sibling=%d other=%d", resp.StatusCode, firstCalls, siblingCalls, otherCalls)
	}
}

func TestUnavailableModelIsRetiredAndReported(t *testing.T) {
	var deniedCalls, fallbackCalls int
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deniedCalls++
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"your current account does not have access to this model"}}`))
	}))
	defer denied.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer fallback.Close()

	var unavailable Upstream
	srv := New(Config{
		Provider: "modelscope",
		BaseURL:  denied.URL,
		APIKey:   "ms-key",
		Model:    "gated-model",
		Fallbacks: []Upstream{{
			Provider: "opencode-go", BaseURL: fallback.URL, APIKey: "zen-key", Model: "zen-free",
		}},
		OnUnavailable: func(route Upstream) { unavailable = route },
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
			t.Fatalf("request %d status = %d", i+1, resp.StatusCode)
		}
	}
	if deniedCalls != 1 || fallbackCalls != 2 {
		t.Fatalf("calls denied=%d fallback=%d", deniedCalls, fallbackCalls)
	}
	if unavailable.Provider != "modelscope" || unavailable.Model != "gated-model" {
		t.Fatalf("unavailable callback = %+v", unavailable)
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

// TestErrorBodyWith200Rotates verifies that a gateway which serves an error
// object with HTTP 200 (e.g. ModelScope's insufficient_quota) rotates to the
// next route instead of handing Claude Code an empty success.
func TestErrorBodyWith200Rotates(t *testing.T) {
	var errorCalls, fallbackCalls int
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errorCalls++
		w.Write([]byte(`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`))
	}))
	defer errSrv.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer fallback.Close()

	srv := New(Config{
		Provider: "modelscope",
		BaseURL:  errSrv.URL,
		APIKey:   "ms-key",
		Model:    "gated-model",
		Fallbacks: []Upstream{{
			Provider: "opencode-go", BaseURL: fallback.URL, APIKey: "zen-key", Model: "zen-free",
		}},
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
		t.Fatalf("status = %d, want 200 from fallback", resp.StatusCode)
	}
	if errorCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("calls error=%d fallback=%d", errorCalls, fallbackCalls)
	}
}

// TestDegenerate200TemporarilyRotates verifies that an empty completion
// (choices:null served with HTTP 200) rotates to the fallback for this request
// but retries the selected model on the next turn. A transient empty response
// must not poison the proxy until ultra-zen is restarted.
func TestDegenerate200TemporarilyRotates(t *testing.T) {
	var emptyCalls int
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emptyCalls++
		w.Write([]byte(`{"id":"","object":"","created":0,"model":"dud","choices":null,"usage":{"prompt_tokens":0,"completion_tokens":0}}`))
	}))
	defer empty.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer fallback.Close()

	var unavailable Upstream
	srv := New(Config{
		Provider: "modelscope",
		BaseURL:  empty.URL,
		APIKey:   "ms-key",
		Model:    "dud-model",
		Fallbacks: []Upstream{{
			Provider: "opencode-go", BaseURL: fallback.URL, APIKey: "zen-key", Model: "zen-free",
		}},
		OnUnavailable: func(route Upstream) { unavailable = route },
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
			t.Fatalf("request %d status = %d", i+1, resp.StatusCode)
		}
	}
	if emptyCalls != 2 {
		t.Fatalf("selected route called %d times, want 2 (retried on next turn)", emptyCalls)
	}
	if unavailable.Provider != "" || unavailable.Model != "" {
		t.Fatalf("transient empty response invoked unavailable callback: %+v", unavailable)
	}
}

// TestClassifySSEKeepAliveIsNotDegenerate reproduces the field bug that killed
// healthy free models: a gateway opening a stream with ": keep-alive" comments
// then a usage-only chunk carrying an empty "choices" must be classified bodyOK,
// not bodyDegenerate (which would unnecessarily rotate away from the route).
func TestClassifySSEKeepAliveIsNotDegenerate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "keep-alive comments then real data chunk",
			body: ": keep-alive\n: keep-alive\ndata: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			want: bodyOK,
		},
		{
			name: "keep-alive then usage-only empty choices chunk",
			body: ": keep-alive\ndata: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":10}}\n\ndata: [DONE]\n\n",
			want: bodyOK,
		},
		{
			name: "event framing with keep-alive prefix",
			body: ": keep-alive\n: keep-alive\nevent: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n",
			want: bodyOK,
		},
		{
			name: "plain choices-null JSON is still degenerate",
			body: `{"id":"","object":"","created":0,"model":"dud","choices":null,"usage":{"prompt_tokens":0}}`,
			want: bodyDegenerate,
		},
		{
			name: "error object with empty choices is bodyError",
			body: `{"error":{"code":"insufficient_quota","message":"quota"}}`,
			want: bodyError,
		},
		{
			name: "error object plus empty choices is bodyError not degenerate",
			body: `{"error":{"message":"model access denied"},"choices":[]}`,
			want: bodyError,
		},
		{
			name: "real completion JSON is bodyOK",
			body: `{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			want: bodyOK,
		},
		{
			name: "empty prefix is degenerate",
			body: "",
			want: bodyDegenerate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyUpstreamBody([]byte(tc.body)); got != tc.want {
				t.Fatalf("classifyUpstreamBody = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestClassifyUpstreamBodyWithKeepAliveInFullStream verifies the exact proxy-log
// shape that retired laguna-s-2.1-free at 13:39:07: leading ": keep-alive" lines
// then a real data chunk. The old classifier misread this as degenerate because
// it compared the raw prefix against "data:" before stripping comments.
func TestClassifyUpstreamBodyWithKeepAliveInFullStream(t *testing.T) {
	body := []byte(": keep-alive\n: keep-alive\ndata: {\"id\":\"chatcmpl-x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n")
	if got := classifyUpstreamBody(body); got != bodyOK {
		t.Fatalf("classifyUpstreamBody(keepalive stream) = %d, want bodyOK", got)
	}
}

// Test503RotatesToFallback verifies that an upstream 503 "Endpoint is unavailable"
// is treated as a temporary outage: the failed route is soft-skipped (NOT promoted)
// and the next healthy pool route serves the request. Before the fix, a 503 was
// returned straight to the client as a hard API error.
func Test503RotatesToFallback(t *testing.T) {
	var selCalls, fbCalls int
	sel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selCalls++
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"type":"server_error","message":"Endpoint is unavailable."}}`))
	}))
	defer sel.Close()
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fbCalls++
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"fallback-ok"}}]}`))
	}))
	defer fb.Close()

	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  fb.URL,
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: sel.URL, APIKey: "k", Model: "glm-5.2"},
			{Provider: "opencode-go", BaseURL: fb.URL, APIKey: "k", Model: "north-mini-code-free"},
		},
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 after 503 rotation, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fallback-ok") {
		t.Fatalf("expected fallback response, got %s", rec.Body.String())
	}
	if selCalls == 0 || fbCalls == 0 {
		t.Fatalf("expected 503 route (sel=%d) to be tried then fallback (fb=%d) to serve", selCalls, fbCalls)
	}
}

// TestAll503RetriesThenReturns verifies that when every pool route 503s, the
// proxy retries the whole pool with backoff and then surfaces the last upstream
// 503 to the client — a real 503, not a bogus 429.
func TestAll503RetriesThenReturns(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"type":"server_error","message":"Endpoint is unavailable."}}`))
	}))
	defer up.Close()

	cfg := Config{
		Provider:         "opencode-go",
		BaseURL:          up.URL,
		APIKey:           "k",
		Model:            "deepseek-v4-flash",
		RateLimitRetries: 1,
		RateLimitBackoff: time.Millisecond,
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"deepseek-v4-flash","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 after retries, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Endpoint is unavailable") {
		t.Fatalf("expected the upstream 503 body, got %s", rec.Body.String())
	}
}

// TestTransportErrorRotatesToFallback verifies that a transport failure (no HTTP
// response, e.g. connection refused) on one route soft-skips to the next healthy
// pool route instead of surfacing a 502.
func TestTransportErrorRotatesToFallback(t *testing.T) {
	// A closed listener: connection refused.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"fallback-ok"}}]}`))
	}))
	defer fb.Close()

	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  fb.URL,
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: deadURL, APIKey: "k", Model: "glm-5.2"},
			{Provider: "opencode-go", BaseURL: fb.URL, APIKey: "k", Model: "north-mini-code-free"},
		},
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 after transport-error rotation, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fallback-ok") {
		t.Fatalf("expected fallback response, got %s", rec.Body.String())
	}
}
