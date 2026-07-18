package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
