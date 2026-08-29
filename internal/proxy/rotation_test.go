package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goodBody is a minimal valid OpenAI completion for fake upstreams.
const goodBody = `{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// poolFor builds a 3-route config: launch primary primary-model (baseA),
// fallback glm-5.2 (baseB), fallback zen-free (baseC).
func poolFor(baseA, baseB, baseC string) Config {
	return Config{
		Provider: "opencode-go",
		BaseURL:  baseA,
		APIKey:   "k",
		Model:    "primary-model",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: baseB, APIKey: "k", Model: "glm-5.2"},
			{Provider: "opencode-go", BaseURL: baseC, APIKey: "k", Model: "zen-free"},
		},
		Port:             0,
		RateLimitRetries: 0,
	}
}

// activeRouteOf reads the pool cursor under lock (test-side observability of
// promoteRoute/limitRoute without exporting anything).
func activeRouteOf(s *Server) int {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	return s.activeRoute
}

func TestIsTransientUpstreamFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"zen-go model unavailable", `{"type":"server_error","message":"Error from provider (Console): Upstream request failed: Model is unavailable."}`, true},
		{"model unavailable alone", `{"error":{"message":"Model is unavailable"}}`, true},
		{"upstream failed with server_error", `{"type":"server_error","message":"Upstream request failed"}`, true},
		{"mixed case", `{"type":"SERVER_ERROR","message":"UPSTREAM REQUEST FAILED"}`, true},
		// Request-shaped 400s must NOT rotate — halving owns them.
		{"param error", `{"error":{"message":"Error from provider: Upstream request failed","type":"invalid_request_error","param":"max_tokens"}}`, false},
		{"context length", `{"error":{"message":"This model's maximum context length is 8192 tokens","type":"invalid_request_error"}}`, false},
		{"data inspection", `{"error":{"message":"data_inspection_failed","type":"invalid_request_error"}}`, false},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientUpstreamFailure([]byte(tc.body)); got != tc.want {
				t.Fatalf("isTransientUpstreamFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestTransient400RotatesWithoutPromoting covers fix #2: the zen-go 400
// "Upstream request failed: Model is unavailable" is a transient availability
// failure, so the pool rotates to the next route, the dead route is NOT
// promoted (a promote would pin it head-of-line), and the following turn must
// not re-probe it either (the park keeps it skipped).
func TestTransient400RotatesWithoutPromoting(t *testing.T) {
	var deadCalls, fbCalls int
	clock := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadCalls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type":"server_error","message":"Error from provider (Console): Upstream request failed: Model is unavailable."}`))
	}))
	defer dead.Close()
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fbCalls++
		w.Write([]byte(goodBody))
	}))
	defer fb.Close()

	s := New(Config{
		Provider:  "opencode-go",
		BaseURL:   dead.URL,
		APIKey:    "k",
		Model:     "glm-5.2", // the pool primary IS the availability-broken route
		Fallbacks: []Upstream{{Provider: "opencode-go", BaseURL: fb.URL, APIKey: "k", Model: "zen-free"}},
		Port:      0,
	})
	s.now = func() time.Time { return clock }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
			`{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		rec := httptest.NewRecorder()
		s.handleMessages(rec, req)
		return rec.Code
	}

	if code := send(); code != 200 {
		t.Fatalf("turn 1 status = %d, want 200 from the fallback, body=%s", code, "")
	}
	if deadCalls != 1 || fbCalls != 1 {
		t.Fatalf("turn 1 calls dead=%d fb=%d, want 1/1 (rotate without promote/halving)", deadCalls, fbCalls)
	}
	if ar := activeRouteOf(s); ar == 0 {
		t.Fatalf("pool cursor = 0: the transient 400 promoted the dead route head-of-line")
	}
	// The next turn must start elsewhere: the parked route is skipped, so the
	// broken upstream receives no new probes ("131 hits" regression).
	if code := send(); code != 200 {
		t.Fatalf("turn 2 status = %d, want 200", code)
	}
	if deadCalls != 1 {
		t.Fatalf("transient-400 route probed again while parked (calls=%d), want still 1", deadCalls)
	}
	if fbCalls != 2 {
		t.Fatalf("fallback calls = %d, want 2 (fallback serves both turns)", fbCalls)
	}
}

// TestRequestShaped400StillReachesHalving covers the other half of fix #2:
// a param-named invalid_request_error 400 must keep flowing to handleMessages'
// same-params + halved-max_tokens retry on the SAME upstream.
func TestRequestShaped400StillReachesHalving(t *testing.T) {
	var calls int
	var gotMax int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotMax = req.MaxTokens
		calls++
		if calls <= 2 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"max_tokens is too large","type":"invalid_request_error","param":"max_tokens"}}`))
			return
		}
		w.Write([]byte(goodBody))
	}))
	defer up.Close()

	s := New(Config{Provider: "p", BaseURL: up.URL, APIKey: "k", Model: "m", Port: 0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(s.BaseURL()+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"m","max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after halved retry", resp.StatusCode)
	}
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want 3 (initial + same params + halved)", calls)
	}
	if gotMax != maxOutputTokens/2 {
		t.Fatalf("third call max_tokens = %d, want halved %d", gotMax, maxOutputTokens/2)
	}
}

// TestCanceledContextDoesNotReorderPool covers fix #3: Claude Code cancels a
// mid-flight turn; the resulting context.Canceled transport error must NOT
// soft-limit the in-flight route, park it, or produce a throttle log. The
// pool state must be untouched.
func TestCanceledContextDoesNotReorderPool(t *testing.T) {
	var slowHits, otherHits int
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHits++
		select {
		case <-time.After(3 * time.Second):
			w.Write([]byte(goodBody))
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits++
		w.Write([]byte(goodBody))
	}))
	defer other.Close()

	s := New(poolFor(slow.URL, slow.URL, other.URL))
	// primary route = slow (idx0), fallback glm-5.2 also slow (idx1) so the
	// cancel happens on a route that WOULD have been soft-limited before.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"primary-model","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	cctx, ccancel := context.WithCancel(context.Background())
	req = req.WithContext(cctx)
	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		s.handleMessages(rec, req)
	}()
	time.Sleep(100 * time.Millisecond)
	ccancel()
	<-done

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for a canceled request", rec.Code)
	}
	if ar := activeRouteOf(s); ar != 0 {
		t.Fatalf("pool cursor moved to %d on a canceled request: the cancel reordered the pool", ar)
	}
	s.poolMu.Lock()
	parked, strikes := append([]time.Time{}, s.nextEligible...), append([]int{}, s.strikes...)
	s.poolMu.Unlock()
	for i := range parked {
		if !parked[i].IsZero() || strikes[i] != 0 {
			t.Fatalf("route %d was limited/parked by a canceled request: parked=%v strikes=%v", i, parked, strikes)
		}
	}
	if otherHits != 0 {
		t.Fatalf("rotation continued after cancel (%d calls); want the cancel to break out immediately", otherHits)
	}
}

// TestCooldownExpiryReadmitsRoute covers fix #7's park window: a route that
// returns a temporary 429 is parked (5 minutes) and skipped by subsequent
// turns; after the park expires it becomes eligible again and is probed once
// more. Uses the clock seam.
func TestCooldownExpiryReadmitsRoute(t *testing.T) {
	var throttleCalls, healthyCalls int
	clock := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	throttle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		throttleCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"provider_rate_limit_exceeded"}}`))
	}))
	defer throttle.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyCalls++
		w.Write([]byte(goodBody))
	}))
	defer healthy.Close()

	s := New(Config{
		Provider:  "opencode-go",
		BaseURL:   throttle.URL,
		APIKey:    "k",
		Model:     "glm-5.2",
		Fallbacks: []Upstream{{Provider: "opencode-go", BaseURL: healthy.URL, APIKey: "k", Model: "zen-free"}},
		Port:      0,
	})
	s.now = func() time.Time { return clock }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
			`{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		rec := httptest.NewRecorder()
		s.handleMessages(rec, req)
		return rec.Code
	}

	if code := send(); code != 200 {
		t.Fatalf("turn 1 status = %d, want 200", code)
	}
	if throttleCalls != 1 {
		t.Fatalf("throttle route probed %d times on turn 1, want 1", throttleCalls)
	}
	// Turns 2..N inside the 5-minute park: no re-probes at all.
	for i := 0; i < 4; i++ {
		clock = clock.Add(3 * time.Second)
		if code := send(); code != 200 {
			t.Fatalf("turn %d status = %d", i+2, code)
		}
	}
	if throttleCalls != 1 {
		t.Fatalf("throttle route re-probed while parked (calls=%d, want still 1)", throttleCalls)
	}
	// Past the park: the route is admitted again and gets exactly one probe.
	clock = clock.Add(5 * time.Minute)
	if code := send(); code != 200 {
		t.Fatalf("post-expiry turn status = %d", code)
	}
	if throttleCalls != 2 {
		t.Fatalf("throttle route calls = %d after cooldown expiry, want 2 (re-admitted)", throttleCalls)
	}
}

// TestRepeatOffenseBackoffLadder pins the cooldown TTL ladder: 1st offense 5
// minutes, 2nd 15, 3rd+ 60 (capped); a success clears the history so the next
// incident starts at 5 again.
func TestRepeatOffenseBackoffLadder(t *testing.T) {
	clock := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	s := New(Config{
		Provider:  "p",
		BaseURL:   "http://127.0.0.1:1",
		APIKey:    "k",
		Model:     "m",
		Fallbacks: []Upstream{{Provider: "p", BaseURL: "http://127.0.0.1:2", APIKey: "k", Model: "m2"}},
		Port:      0,
	})
	s.now = func() time.Time { return clock }

	eligible := func() bool {
		s.poolMu.Lock()
		defer s.poolMu.Unlock()
		return s.routeEligibleLocked(0)
	}
	parkedUntil := func() time.Time {
		s.poolMu.Lock()
		defer s.poolMu.Unlock()
		return s.nextEligible[0]
	}

	if !eligible() {
		t.Fatal("fresh route should be eligible")
	}
	s.parkRoute(0)
	if d := parkedUntil().Sub(clock); d != 5*time.Minute {
		t.Fatalf("1st offense cooldown = %s, want 5m", d)
	}
	if eligible() {
		t.Fatal("parked route should read ineligible")
	}
	clock = clock.Add(2 * time.Minute)
	s.parkRoute(0)
	if d := parkedUntil().Sub(clock); d != 15*time.Minute {
		t.Fatalf("2nd offense cooldown = %s, want 15m", d)
	}
	clock = clock.Add(2 * time.Minute)
	s.parkRoute(0)
	if d := parkedUntil().Sub(clock); d != time.Hour {
		t.Fatalf("3rd offense cooldown = %s, want 60m", d)
	}
	clock = clock.Add(2 * time.Minute)
	s.parkRoute(0)
	if d := parkedUntil().Sub(clock); d != time.Hour {
		t.Fatalf("4th offense cooldown = %s, want 60m (capped)", d)
	}
	s.clearRouteCooldown(0)
	if !eligible() {
		t.Fatal("cooldown-clear should re-admit")
	}
	s.parkRoute(0)
	if d := parkedUntil().Sub(clock); d != 5*time.Minute {
		t.Fatalf("cooldown after a success = %s, want 5m (strike count reset)", d)
	}
}

// TestPromoteOnlyOnGood200 covers fix #3's bookkeeping gate: a non-200 that
// passes through (here a 404) must not promoteRoute, must not clear/book any
// usage, and must not touch the exhaustion flag for its provider.
func TestPromoteOnlyOnGood200(t *testing.T) {
	var badHits, okHits int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"no route","type":"invalid_request_error"}}`))
	}))
	defer bad.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits++
		w.Write([]byte(goodBody))
	}))
	defer okSrv.Close()

	s := New(Config{
		Provider: "opencode-go",
		BaseURL:  okSrv.URL,
		APIKey:   "k",
		Model:    "primary-model",
		Fallbacks: []Upstream{
			{Provider: "badprov", BaseURL: bad.URL, APIKey: "k", Model: "glm-5.2"},
			{Provider: "okprov", BaseURL: okSrv.URL, APIKey: "k", Model: "zen-free"},
		},
		Port: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Turn 1: /model selects the 404-ing route. It must be passed through raw
	// and must NOT be promoted — the cursor stays on the healthy primary (0).
	// A promote would make every later request start at the broken route.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s; want the raw 404 passed through", rec.Code, rec.Body.String())
	}
	if badHits != 1 {
		t.Fatalf("broken route hits = %d, want 1", badHits)
	}
	if ar := activeRouteOf(s); ar != 0 {
		t.Fatalf("pool cursor = %d after a 404: the broken route was promoted", ar)
	}
	// And no success bookkeeping happened for its provider.
	for _, row := range s.usage.getRows() {
		if row.Name == "badprov" {
			t.Fatalf("provider %q got success bookkeeping from a 404: %+v", row.Name, row)
		}
	}
	// Turn 2: the healthy primary serves and IS promoted (cursor stays 0, the
	// okprov route at index 2 proves promotion by moving the cursor).
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-okprov-zen-free","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	rec2 := httptest.NewRecorder()
	s.handleMessages(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("turn 2 status = %d, body=%s", rec2.Code, rec2.Body.String())
	}
	if ar := activeRouteOf(s); ar != 2 {
		t.Fatalf("pool cursor = %d after a good 200 on route 2, want 2 (promote-on-success)", ar)
	}
}

// TestThrottleLogOnlyWhenThrottled covers the observability half of fix #3:
// "every available route is throttled" may only fire when at least one route
// actually answered with a 429-class response; a round of transport failures
// gets the honest "failed transiently" wording instead.
func TestThrottleLogOnlyWhenThrottled(t *testing.T) {
	t.Run("transport-only round", func(t *testing.T) {
		dead1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url1 := dead1.URL
		dead1.Close()
		dead2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url2 := dead2.URL
		dead2.Close()

		s := New(Config{
			Provider:         "p",
			BaseURL:          url1,
			APIKey:           "k",
			Model:            "m",
			Fallbacks:        []Upstream{{Provider: "p", BaseURL: url2, APIKey: "k", Model: "m2"}},
			Port:             0,
			RateLimitRetries: 1,
			RateLimitBackoff: time.Millisecond,
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		orig := log.Writer()
		log.SetOutput(&buf)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
			`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		rec := httptest.NewRecorder()
		s.handleMessages(rec, req)
		log.SetOutput(orig)
		out := buf.String()
		if !strings.Contains(out, "failed transiently") {
			t.Fatalf("transport-only round should log the outage wording; got:\n%s", out)
		}
		if strings.Contains(out, "every available route is throttled") {
			t.Fatalf("transport-only round must not claim a throttle; got:\n%s", out)
		}
	})
	t.Run("429 round", func(t *testing.T) {
		throttler := func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
		}
		a := httptest.NewServer(http.HandlerFunc(throttler))
		defer a.Close()
		b := httptest.NewServer(http.HandlerFunc(throttler))
		defer b.Close()

		s := New(Config{
			Provider:         "p",
			BaseURL:          a.URL,
			APIKey:           "k",
			Model:            "m",
			Fallbacks:        []Upstream{{Provider: "p", BaseURL: b.URL, APIKey: "k", Model: "m2"}},
			Port:             0,
			RateLimitRetries: 1,
			RateLimitBackoff: time.Millisecond,
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		orig := log.Writer()
		log.SetOutput(&buf)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
			`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		rec := httptest.NewRecorder()
		s.handleMessages(rec, req)
		log.SetOutput(orig)
		if !strings.Contains(buf.String(), "every available route is throttled") {
			t.Fatalf("a real 429 round must log the throttle line; got:\n%s", buf.String())
		}
	})
}

// TestBackoffBreakLogOnHugeRetryAfter covers observability (c): when a
// Retry-After exceeds the wait ceiling and the backoff loop breaks, that exit
// must be logged.
func TestBackoffBreakLogOnHugeRetryAfter(t *testing.T) {
	var calls int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer up.Close()

	s := New(Config{Provider: "p", BaseURL: up.URL, APIKey: "k", Model: "m", Port: 0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)
	log.SetOutput(orig)

	if !strings.Contains(buf.String(), "exceeds") {
		t.Fatalf("expected a log when the backoff break fires; got:\n%s", buf.String())
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1: a 1-hour Retry-After must stop the retry loop", calls)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the 429 passed through", rec.Code)
	}
}

// TestWriteErrorExitsAreLogged covers observability (b): every error response
// written by handleMessages leaves a log line.
func TestWriteErrorExitsAreLogged(t *testing.T) {
	s := New(Config{Provider: "p", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m", Port: 0})
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{oops`))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)
	log.SetOutput(orig)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(buf.String(), "request error 400") {
		t.Fatalf("writeError exit was not logged; got:\n%s", buf.String())
	}
}

// TestDumpFailingRequestStampsUsedRoute covers observability (d): the last-400
// dump must record the route that actually failed, not the launch config.
func TestDumpFailingRequestStampsUsedRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var calls int
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"max_tokens is too large","type":"invalid_request_error","param":"max_tokens"}}`))
	}))
	defer failed.Close()
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("primary route should not be reached by this test")
	}))
	defer primarySrv.Close()

	s := New(Config{
		Provider:  "opencode-go",
		BaseURL:   primarySrv.URL,
		APIKey:    "k",
		Model:     "primary-model",
		Fallbacks: []Upstream{{Provider: "opencode-go", BaseURL: failed.URL, APIKey: "k", Model: "glm-5.2"}},
		Port:      0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"glm-5.2","max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)
	if calls < 2 {
		t.Fatalf("upstream calls = %d, want at least 2 (initial + retry) so a dump was written", calls)
	}

	data, err := os.ReadFile(filepath.Join(home, ".cache/ultra-zen/last-400.json"))
	if err != nil {
		t.Fatalf("dump not written: %v", err)
	}
	var dump struct {
		Model    string `json:"model"`
		Upstream string `json:"upstream"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("dump not JSON: %v", err)
	}
	if dump.Model != "glm-5.2" {
		t.Fatalf("dump model = %q, want the actually-used route model glm-5.2", dump.Model)
	}
	if dump.Upstream != failed.URL {
		t.Fatalf("dump upstream = %q, want the actually-used base %q", dump.Upstream, failed.URL)
	}
}
