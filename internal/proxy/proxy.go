package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config holds the gateway target and credentials for the proxy.
type Config struct {
	Provider         string // provider name for the primary route
	BaseURL          string // e.g. https://opencode.ai/zen/go/v1
	APIKey           string
	Model            string         // the Zen model id to forward orchestrator requests to
	WorkerModel      string         // if set, background sub-agents use this cheaper model
	Fallbacks        []Upstream     // ordered free-model fallbacks; replaces worker routing when set
	OpenRouterRPM    int            // session-wide request pace for OpenRouter free models
	RateLimitRetries int            // full-pool retries after temporary 429s; zero uses the default
	RateLimitBackoff time.Duration  // initial temporary-429 backoff; zero uses the default
	Port             int            // local listen port
	Models           []ModelInfo    // full model list advertised at /v1/models
	Upstreams        []Upstream     // every known upstream route (primary + fallbacks); maps /model ids to gateways
	OnUnavailable    func(Upstream) // called after an explicit per-model access denial
}

// Upstream identifies one model and the endpoint/key that serves it. Fallbacks
// may live on a different gateway from the primary model, which lets a Zen free
// model rotate to an explicitly selected OpenRouter free model without
// restarting Claude Code.
type Upstream struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
}

// ModelInfo is a minimal model entry for /v1/models advertising.
type ModelInfo struct {
	ID   string
	Name string
}

// maxOutputTokens is the maximum max_tokens the proxy forwards to the Zen
// gateway. Claude Code often requests a very large max_tokens (e.g. 512000 or
// more) to avoid truncating long agent outputs; the Zen gateway rejects any
// value above the model's real output limit with a 400 "Upstream request
// failed". Clamping to a safe ceiling avoids that class of 400 entirely. 65536
// is well within every current Zen model's output budget and generous enough
// for long agent/tool-use responses.
const maxOutputTokens = 65536

// Server is the in-process Anthropic->OpenAI bridge.
type Server struct {
	cfg            Config
	srv            *http.Server
	baseURL        string // resolved address, e.g. http://127.0.0.1:38271
	poolMu         sync.Mutex
	activeRoute    int
	exhaustedRoute []bool
	gateMu         sync.Mutex
	nextOpenRouter time.Time
	// modelRoute maps every id Claude Code's /model command can hand us (both
	// the plain Zen id and the provider-qualified id) to the upstream that
	// serves it. It is built once at New from cfg.Upstreams.
	modelRoute map[string]Upstream
}

const (
	defaultRateLimitRetries = 3
	defaultRateLimitBackoff = 2 * time.Second
	maxRateLimitWait        = 2 * time.Minute
)

// New creates a proxy listening on the given port. Call Start to run it.
// A Port of 0 lets the OS assign a free port, which allows many ultra-zen
// instances to run concurrently without port collisions.
func New(cfg Config) *Server {
	s := &Server{
		cfg:            cfg,
		exhaustedRoute: make([]bool, 1+len(cfg.Fallbacks)),
		modelRoute:     buildModelRoute(cfg),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleHealth) // any other path -> health
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Start runs the proxy in the background and returns once it is accepting
// connections, or an error. The server runs until ctx is cancelled / Shutdown
// is called.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.baseURL = "http://" + ln.Addr().String()
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shCtx)
	}()
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("ultra-zen proxy: %v", err)
		}
	}()
	return nil
}

// BaseURL returns the local address the proxy is reachable on.
func (s *Server) BaseURL() string { return s.baseURL }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

// handleModels advertises every usable model at GET /v1/models so Claude
// Code's /model command shows the full list. The proxy's runtime routing
// (orchestrator/worker) still applies per-request based on tool classification;
// the advertised list is independent.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Claude Code is an Anthropic client, so return the Anthropic /v1/models
	// shape: each entry has type/id/display_name/created_at, and the list has
	// has_more/first_id/last_id. The OpenAI-style object/owned_by fields are
	// included too so OpenAI-compatible probes still work.
	entry := func(id, name string) map[string]any {
		if name == "" {
			name = id
		}
		return map[string]any{
			"type":         "model",
			"id":           id,
			"display_name": name,
			"created_at":   "2026-01-01T00:00:00Z",
			"object":       "model",
			"owned_by":     "ultra-zen",
		}
	}
	var models []map[string]any
	for _, m := range s.cfg.Models {
		models = append(models, entry(m.ID, m.Name))
	}
	if len(models) == 0 {
		models = append(models, entry(s.cfg.Model, ""))
	}
	out := map[string]any{
		"object":   "list",
		"data":     models,
		"has_more": false,
	}
	if len(models) > 0 {
		out["first_id"] = models[0]["id"]
		out["last_id"] = models[len(models)-1]["id"]
	}
	body, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// buildModelRoute maps model ids to the upstream that serves them so the
// proxy can honor Claude Code's /model command (which sends the chosen id as
// the request's "model" field). Keys are the plain Zen id ("deepseek-v4-flash")
// and the provider-qualified id ("opencode-go/deepseek-v4-flash-free",
// "openrouter/poolside/laguna-s-2.1:free"). Primary + fallback upstreams are
// all registered; the primary's plain id maps to the primary route so the
// default selection keeps working.
func buildModelRoute(cfg Config) map[string]Upstream {
	m := make(map[string]Upstream)
	add := func(u Upstream) {
		if u.Model == "" {
			return
		}
		// Plain Zen id, and the provider-qualified spelling so both forms a
		// /model switch might send resolve to the same upstream.
		m[u.Model] = u
		if u.Provider != "" {
			m[u.Provider+"/"+u.Model] = u
		}
	}
	add(Upstream{Provider: cfg.Provider, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model})
	for _, f := range cfg.Fallbacks {
		add(f)
	}
	return m
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 400, "invalid_request_error", "could not read request body: "+err.Error())
		return
	}
	var areq anthropicRequest
	if err := json.Unmarshal(body, &areq); err != nil {
		writeError(w, 400, "invalid_request_error", "invalid Anthropic request: "+err.Error())
		return
	}
	// Model routing. When Claude Code's /model command selects a model, the
	// request body carries the chosen id; honor it by resolving to that model's
	// upstream. When no /model switch is active, fall back to the launch-time
	// split: an explicitly configured worker model serves background sub-agents
	// (requests without interactive tools), the main model serves the rest. A
	// fallback pool replaces the worker split and gives every role the same
	// resilient route, so a main-loop or subagent request can continue on the
	// next free model without restarting the session.
	primary := Upstream{Provider: s.cfg.Provider, BaseURL: s.cfg.BaseURL, APIKey: s.cfg.APIKey, Model: s.cfg.Model}
	if len(s.cfg.Fallbacks) == 0 && s.cfg.WorkerModel != "" && !hasInteractiveTools(areq.Tools) {
		// Background sub-agent: run on the worker unless the user explicitly
		// selected a model via /model, which wins.
		if u, ok := s.modelRoute[areq.Model]; ok {
			primary = u
		} else {
			primary.Model = s.cfg.WorkerModel
		}
	} else if areq.Model != "" {
		if u, ok := s.modelRoute[areq.Model]; ok {
			primary = u
		}
	}
	oreq, err := areq.toOpenAI(primary.Model)
	if err != nil {
		writeError(w, 400, "invalid_request_error", err.Error())
		return
	}
	// Clamp max_tokens to a safe ceiling. Claude Code frequently requests very
	// large values that the Zen gateway rejects with a 400.
	if oreq.MaxTokens > maxOutputTokens || oreq.MaxTokens <= 0 {
		oreq.MaxTokens = maxOutputTokens
	}

	payload, resp, used, err := s.forwardWithRateLimit(r.Context(), primary, oreq)
	if err != nil {
		writeError(w, 502, "api_error", "gateway request failed: "+err.Error())
		return
	}
	if resp == nil {
		writeError(w, 429, "rate_limit_error", "every configured free model is exhausted for this session")
		return
	}
	defer resp.Body.Close()

	// On a 400 "Upstream request failed", retry. Two strategies:
	//   1. Same params (handles transient backend failures).
	//   2. Halve max_tokens (handles oversized-token 400s).
	if resp.StatusCode == http.StatusBadRequest {
		ub, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.dumpFailingRequest(ub, payload)
		log.Printf("ultra-zen proxy: upstream 400 (max_tokens=%d): %s | request: %s", oreq.MaxTokens, truncate(string(ub), 200), truncate(string(payload), 500))

		// First retry: same params (handles transient backend failures).
		_, resp2, err := s.forwardTo(r.Context(), used, oreq)
		if err != nil {
			writeError(w, 502, "api_error", "gateway retry failed: "+err.Error())
			return
		}
		if resp2.StatusCode == http.StatusOK {
			resp.Body.Close()
			resp.Body = resp2.Body
			resp.StatusCode = resp2.StatusCode
			resp.Header = resp2.Header
			log.Printf("ultra-zen proxy: retry (same params) succeeded")
		} else {
			ub2, _ := io.ReadAll(resp2.Body)
			resp2.Body.Close()
			log.Printf("ultra-zen proxy: retry (same params) failed (%d): %s", resp2.StatusCode, truncate(string(ub2), 200))
			if oreq.MaxTokens > 1024 {
				oreq.MaxTokens /= 2
				payload3, resp3, err := s.forwardTo(r.Context(), used, oreq)
				if err != nil {
					writeError(w, 502, "api_error", "gateway retry failed: "+err.Error())
					return
				}
				if resp3.StatusCode == http.StatusOK {
					resp.Body.Close()
					resp.Body = resp3.Body
					resp.StatusCode = resp3.StatusCode
					resp.Header = resp3.Header
					log.Printf("ultra-zen proxy: retry (halved max_tokens=%d) succeeded", oreq.MaxTokens)
				} else {
					ub3, _ := io.ReadAll(resp3.Body)
					resp3.Body.Close()
					log.Printf("ultra-zen proxy: retry (halved) also failed (%d): %s | request: %s", resp3.StatusCode, truncate(string(ub3), 200), truncate(string(payload3), 500))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(resp3.StatusCode)
					w.Write(ub3)
					return
				}
			} else {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(resp2.StatusCode)
				w.Write(ub2)
				return
			}
		}
	} else if resp.StatusCode != http.StatusOK {
		ub, _ := io.ReadAll(resp.Body)
		log.Printf("ultra-zen proxy: upstream %d: %s | request: %s", resp.StatusCode, truncate(string(ub), 200), truncate(string(payload), 500))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(ub)
		return
	}

	if areq.Stream {
		s.streamResponse(w, resp, areq.Model)
		return
	}
	s.nonStreamResponse(w, resp, areq.Model)
}

// routeChoice retains a route's stable pool index so concurrent requests can
// promote a working fallback or retire a model whose free allocation ended.
type routeChoice struct {
	Upstream
	index int
}

// forwardWithRateLimit sends a request through the current route and rotates
// through the configured pool on 429. A FreeUsageLimitError retires that free
// provider for the rest of the Claude Code session; ordinary model throttles are
// retried with Retry-After/exponential backoff. This prevents a transient 429
// from being handed directly to Claude Code, where it would terminate the
// affected subagent.
func (s *Server) forwardWithRateLimit(ctx context.Context, primary Upstream, oreq *openAIRequest) (payload []byte, resp *http.Response, used Upstream, err error) {
	retries := s.cfg.RateLimitRetries
	if retries == 0 {
		retries = defaultRateLimitRetries
	} else if retries < 0 {
		retries = 0
	}
	backoff := s.cfg.RateLimitBackoff
	if backoff <= 0 {
		backoff = defaultRateLimitBackoff
	}

	var lastPayload, lastBody []byte
	var lastResp *http.Response
	var lastUsed Upstream
	var lastRetryAfter time.Duration

	for round := 0; round <= retries; round++ {
		routes := s.routeOrder(primary)
		if len(routes) == 0 {
			break
		}
		temporary := false
		for _, choice := range routes {
			if s.routeExhausted(choice.index) {
				continue
			}
			if err := s.waitOpenRouterSlot(ctx, choice.BaseURL); err != nil {
				return nil, nil, Upstream{}, err
			}
			oreq.Model = choice.Model
			p, candidate, callErr := s.forwardTo(ctx, choice.Upstream, oreq)
			if callErr != nil {
				return nil, nil, Upstream{}, callErr
			}
			if candidate.StatusCode == http.StatusOK {
				// Peek the body so a gateway error or empty completion served
				// with HTTP 200 rotates to the next route instead of producing
				// an empty assistant turn. The prefix is rewound, so a real
				// completion/stream is passed through untouched.
				prefix := make([]byte, 64*1024)
				n, _ := io.ReadFull(candidate.Body, prefix)
				prefix = prefix[:n]
				candidate.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), candidate.Body))
				switch classifyUpstreamBody(prefix) {
				case bodyError:
					if isFreeUsageLimit(prefix) {
						s.exhaustProviderRoutes(choice.Upstream)
					} else if isModelAccessDenied(prefix) {
						s.limitRoute(choice.index, true)
						if s.cfg.OnUnavailable != nil {
							s.cfg.OnUnavailable(choice.Upstream)
						}
					} else {
						s.limitRoute(choice.index, false)
					}
					log.Printf("ultra-zen proxy: upstream error body on %s: %s; rotating", choice.Model, truncate(string(prefix), 200))
					lastPayload, lastBody, lastResp, lastUsed = p, prefix, candidate, choice.Upstream
					candidate.Body.Close()
					continue
				case bodyDegenerate:
					s.limitRoute(choice.index, true)
					if s.cfg.OnUnavailable != nil {
						s.cfg.OnUnavailable(choice.Upstream)
					}
					log.Printf("ultra-zen proxy: empty completion from %s; retiring route (%s)", choice.Model, truncate(string(prefix), 200))
					candidate.Body.Close()
					continue
				}
			}
			if candidate.StatusCode == http.StatusForbidden {
				body, _ := io.ReadAll(candidate.Body)
				candidate.Body.Close()
				candidate.Body = io.NopCloser(bytes.NewReader(body))
				if isModelAccessDenied(body) {
					s.limitRoute(choice.index, true)
					if s.cfg.OnUnavailable != nil {
						s.cfg.OnUnavailable(choice.Upstream)
					}
					lastPayload, lastBody, lastResp, lastUsed = p, body, candidate, choice.Upstream
					log.Printf("ultra-zen proxy: model unavailable for this account: %s; retiring route (%s)", choice.Model, truncate(string(body), 200))
					continue
				}
			}
			if candidate.StatusCode != http.StatusTooManyRequests {
				s.promoteRoute(choice.index)
				return p, candidate, choice.Upstream, nil
			}

			body, _ := io.ReadAll(candidate.Body)
			candidate.Body.Close()
			candidate.Body = io.NopCloser(bytes.NewReader(body))
			accountExhausted := isOpenRouterDailyLimit(body)
			providerExhausted := accountExhausted || isFreeUsageLimit(body)
			if providerExhausted {
				s.exhaustProviderRoutes(choice.Upstream)
			} else {
				s.limitRoute(choice.index, false)
			}
			if !providerExhausted {
				temporary = true
			}
			lastRetryAfter = parseRetryAfter(candidate.Header.Get("Retry-After"))
			lastPayload, lastBody, lastResp, lastUsed = p, body, candidate, choice.Upstream
			kind := "temporary rate limit"
			if accountExhausted {
				kind = "OpenRouter daily free allowance exhausted"
			} else if providerExhausted {
				kind = "provider free allocation exhausted"
			}
			log.Printf("ultra-zen proxy: %s for %s; rotating (%s)", kind, choice.Model, truncate(string(body), 200))
		}

		if !temporary || round == retries {
			break
		}
		delay := lastRetryAfter
		if delay <= 0 {
			delay = backoff * time.Duration(1<<round)
		}
		if delay > maxRateLimitWait {
			break
		}
		log.Printf("ultra-zen proxy: every available route is throttled; retrying in %s", delay)
		if err := sleepContext(ctx, delay); err != nil {
			return nil, nil, Upstream{}, err
		}
	}

	if lastResp != nil {
		lastResp.Body = io.NopCloser(bytes.NewReader(lastBody))
	}
	return lastPayload, lastResp, lastUsed, nil
}

// routeOrder returns every non-exhausted pool route, starting at the most
// recently successful one. Without a pool it returns the request's primary
// route (which may be the legacy worker model).
func (s *Server) routeOrder(primary Upstream) []routeChoice {
	if len(s.cfg.Fallbacks) == 0 {
		return []routeChoice{{Upstream: primary, index: -1}}
	}
	// The pool is a fixed conceptual array: [cfg primary, fallback 0..n].
	// Every route carries its canonical pool index so limit/exhaust marking
	// stays aligned even when /model selects a fallback as this request's
	// primary. exhaustedRoute is sized 1+len(Fallbacks) to match.
	type entry struct {
		u     Upstream
		index int
	}
	pool := make([]entry, 0, 1+len(s.cfg.Fallbacks))
	pool = append(pool, entry{Upstream{Provider: s.cfg.Provider, BaseURL: s.cfg.BaseURL, APIKey: s.cfg.APIKey, Model: s.cfg.Model}, 0})
	for i, f := range s.cfg.Fallbacks {
		pool = append(pool, entry{f, 1 + i})
	}

	s.poolMu.Lock()
	defer s.poolMu.Unlock()

	out := make([]routeChoice, 0, len(pool))
	seen := make(map[int]bool, len(pool))
	// The request's primary (the /model-selected route when one is active)
	// goes first, deduped against the pool so the same gateway isn't tried
	// twice. Everything else is walked in activeRoute rotation order.
	var primIdx = -1
	for _, e := range pool {
		if sameUpstream(e.u, primary) {
			primIdx = e.index
			break
		}
	}
	if primIdx >= 0 && !s.exhaustedRoute[primIdx] {
		out = append(out, routeChoice{Upstream: primary, index: primIdx})
		seen[primIdx] = true
	}
	for offset := 0; offset < len(pool); offset++ {
		idx := (s.activeRoute + offset) % len(pool)
		if seen[idx] {
			continue
		}
		if !s.exhaustedRoute[idx] {
			out = append(out, routeChoice{Upstream: pool[idx].u, index: idx})
		}
	}
	return out
}

// sameUpstream reports whether two upstreams point at the same model on the
// same gateway (ignoring the provider label, which may differ in spelling).
func sameUpstream(a, b Upstream) bool {
	return a.Model == b.Model && a.BaseURL == b.BaseURL && a.APIKey == b.APIKey
}

func (s *Server) limitRoute(index int, permanent bool) {
	if index < 0 {
		return
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if permanent {
		s.exhaustedRoute[index] = true
	}
	for offset := 1; offset <= len(s.exhaustedRoute); offset++ {
		next := (index + offset) % len(s.exhaustedRoute)
		if !s.exhaustedRoute[next] {
			s.activeRoute = next
			return
		}
	}
}

func (s *Server) routeExhausted(index int) bool {
	if index < 0 {
		return false
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	return s.exhaustedRoute[index]
}

// exhaustProviderRoutes opens the circuit for every route on the exhausted
// free provider. Daily allocations belong to the provider/account rather than
// one model, so trying sibling models only wastes requests. A route on the
// other provider remains available and receives the interrupted request.
func (s *Server) exhaustProviderRoutes(current Upstream) {
	if len(s.cfg.Fallbacks) == 0 {
		return
	}
	routes := make([]Upstream, 0, 1+len(s.cfg.Fallbacks))
	routes = append(routes, Upstream{Provider: s.cfg.Provider, BaseURL: s.cfg.BaseURL, APIKey: s.cfg.APIKey, Model: s.cfg.Model})
	routes = append(routes, s.cfg.Fallbacks...)
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	for i, route := range routes {
		if providerFamily(route) == providerFamily(current) &&
			siteOf(route) == siteOf(current) &&
			route.APIKey == current.APIKey {
			s.exhaustedRoute[i] = true
		}
	}
}

func providerFamily(upstream Upstream) string {
	if upstream.Provider != "" {
		return upstream.Provider
	}
	base := strings.ToLower(strings.TrimRight(upstream.BaseURL, "/"))
	switch {
	case strings.Contains(base, "openrouter.ai/"):
		return "openrouter"
	case strings.Contains(base, "opencode.ai/zen"):
		return "opencode"
	default:
		return base
	}
}

// siteOf normalizes the upstream's base URL to its site, so a daily-limit
// exhaust on one provider site (e.g. api-inference.modelscope.ai) does not
// retire a healthy sibling site's route (e.g. api-inference.modelscope.cn),
// which uses an independent allocation even though it shares Provider+APIKey.
func siteOf(upstream Upstream) string {
	return strings.ToLower(strings.TrimRight(upstream.BaseURL, "/"))
}

func (s *Server) promoteRoute(index int) {
	if index < 0 {
		return
	}
	s.poolMu.Lock()
	s.activeRoute = index
	s.poolMu.Unlock()
}

// waitOpenRouterSlot serializes OpenRouter starts to the free tier's documented
// requests-per-minute pace. The pace is account-shared: a flock-protected file
// keyed by API key holds the next committed slot, so concurrent ultra-zen
// processes using the same account collectively respect the real RPM instead of
// each pacing independently at NxRPM. Concurrent Claude subagents also wait
// here instead of bursting into avoidable 429 responses.
func (s *Server) waitOpenRouterSlot(ctx context.Context, baseURL string) error {
	if s.cfg.OpenRouterRPM <= 0 || !strings.Contains(strings.ToLower(baseURL), "openrouter.ai/") {
		return nil
	}
	interval := time.Minute / time.Duration(s.cfg.OpenRouterRPM)
	// The in-process gate keeps this Server's own starts ordered even when the
	// pace file is unavailable; the shared file provides cross-process pacing.
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	return waitAccountOpenRouterSlot(ctx, interval, s.cfg.APIKey)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isFreeUsageLimit(body []byte) bool {
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "freeusagelimiterror") ||
		strings.Contains(msg, "free usage limit") ||
		strings.Contains(msg, "free allocation") ||
		strings.Contains(msg, "gousagelimiterror") ||
		strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "exceeded your current quota")
}

// classifyUpstreamBody peeks at a 200 response body prefix and decides whether
// it is a usable completion, an upstream error object, or a degenerate
// completion with no output at all. Several gateways (e.g. ModelScope) return
// error objects and empty completions with HTTP 200; without this check the
// proxy would hand Claude Code an empty assistant turn instead of rotating.
const (
	bodyOK int = iota
	bodyError
	bodyDegenerate
)

// classifyUpstreamBody peeks at a 200 response body prefix and decides whether
// it is a usable completion, an upstream error object, or a degenerate
// completion with no output at all. Several gateways (e.g. ModelScope) return
// error objects and empty completions with HTTP 200; without this check the
// proxy would hand Claude Code an empty assistant turn instead of rotating.
//
// The classifier is deliberately structural rather than text-substring based.
// Naive substring matching over the raw prefix misclassifies legitimate SSE
// streams: gateways emit ": keep-alive" comment lines before the first real
// data chunk, and a usage-only chunk can carry an empty "choices":[] or
// "choices":null inside the first 64KB. Treating those as degenerate would
// permanently retire a healthy free model (limitRoute permanent + OnUnavailable),
// which is exactly what broke the free cycle in the field.
func classifyUpstreamBody(prefix []byte) int {
	if len(prefix) == 0 {
		return bodyDegenerate
	}
	// SSE streams are identified by their framing, not their chunk content.
	// Strip leading SSE comment/keepalive lines (": keep-alive", blanks) so a
	// stream that opens with keepalive comments is still recognized as a stream
	// and treated as bodyOK no matter what a usage-only chunk inside it says.
	trimmed := bytes.TrimLeft(prefix, " \t\r\n")
	if bytes.HasPrefix(trimmed, []byte(":")) {
		// Strip leading SSE comment/keepalive lines (": keep-alive", blanks) so
		// a stream that opens with keepalive comments is recognized as a stream.
		trimmed = stripSSEComments(trimmed)
	}
	// SSE framing is bodyOK by construction: a real degenerate stream still
	// emits data: lines, so never classify by the content of a chunk. JSON-array
	// framing (OpenAI batch style) is likewise a usable response.
	if bytes.HasPrefix(trimmed, []byte("data:")) ||
		bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("[")) {
		return bodyOK
	}

	// Non-streaming JSON: parse structurally. Only call it degenerate when the
	// top-level choices is null/empty AND there is no top-level error object.
	var payload struct {
		Error   *json.RawMessage `json:"error"`
		Choices json.RawMessage  `json:"choices"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		// Not JSON at all (or a fragment). Treat an explicit error-looking
		// object as error, otherwise assume OK so we never retire on a guess.
		lower := strings.ToLower(string(trimmed))
		if strings.Contains(lower, `"error":`) && !strings.Contains(lower, `"choices":`) {
			return bodyError
		}
		return bodyOK
	}
	if payload.Error != nil {
		return bodyError
	}
	// choices == null, "[]", or absent => no output at all.
	if len(payload.Choices) == 0 || string(payload.Choices) == "null" || string(payload.Choices) == "[]" {
		return bodyDegenerate
	}
	return bodyOK
}

// stripSSEComments removes leading SSE comment lines (": keep-alive", blank
// lines, ": openai beta") so classification sees the first real data line.
func stripSSEComments(p []byte) []byte {
	rest := p
	for {
		line, rem, ok := bytes.Cut(rest, []byte("\n"))
		if !ok {
			return rest
		}
		line = bytes.TrimRight(line, "\r")
		trimmedLine := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trimmedLine, []byte(":")) || len(bytes.TrimSpace(line)) == 0 {
			rest = rem
			continue
		}
		return rem
	}
}

func isModelAccessDenied(body []byte) bool {
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "does not have access to this model") ||
		strings.Contains(msg, "model access denied") ||
		// opencode Zen gates some models by region/account opt-in; the gateway
		// answers 403 with a RegionError body.
		strings.Contains(msg, "requires explicit opt in") ||
		strings.Contains(msg, "only available hosted in")
}

func isOpenRouterDailyLimit(body []byte) bool {
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "free-models-per-day") ||
		strings.Contains(msg, "free models per day") ||
		strings.Contains(msg, "unlock 1000 free model requests")
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

// forwardTo marshals the OpenAI request and sends it to one gateway route,
// returning the marshalled payload and raw upstream response.
func (s *Server) forwardTo(ctx context.Context, upstream Upstream, oreq *openAIRequest) (payload []byte, resp *http.Response, err error) {
	payload, err = json.Marshal(oreq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	upstreamReq.Header.Set("Content-Type", "application/json")
	resp, err = httpClient().Do(upstreamReq)
	return payload, resp, err
}

// truncate returns s trimmed of leading/trailing whitespace, capped at n chars.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// dumpFailingRequest writes the full request payload + upstream error to
// ~/.cache/ultra-zen/last-400.json so the exact failing request can be
// inspected without truncation. Overwrites on each 400.
func (s *Server) dumpFailingRequest(upstreamErr []byte, payload []byte) {
	dir := os.Getenv("HOME") + "/.cache/ultra-zen"
	_ = os.MkdirAll(dir, 0755)
	dump := struct {
		Model       string          `json:"model"`
		Upstream    string          `json:"upstream"`
		MaxTokens   int             `json:"max_tokens"`
		Request     json.RawMessage `json:"request"`
		UpstreamErr json.RawMessage `json:"upstream_error"`
	}{
		Model:       s.cfg.Model,
		Upstream:    s.cfg.BaseURL,
		Request:     json.RawMessage(payload),
		UpstreamErr: json.RawMessage(upstreamErr),
	}
	var p struct {
		MaxTokens int `json:"max_tokens"`
	}
	_ = json.Unmarshal(payload, &p)
	dump.MaxTokens = p.MaxTokens
	b, _ := json.MarshalIndent(dump, "", "  ")
	_ = os.WriteFile(dir+"/last-400.json", b, 0644)
}

func (s *Server) nonStreamResponse(w http.ResponseWriter, resp *http.Response, model string) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, 502, "api_error", "read gateway response: "+err.Error())
		return
	}
	var oresp openAIResponse
	if err := json.Unmarshal(body, &oresp); err != nil {
		writeError(w, 502, "api_error", "parse gateway response: "+err.Error())
		return
	}
	anthropic := oresp.toAnthropic(model)
	out, err := json.Marshal(anthropic)
	if err != nil {
		// Never hand Claude Code a 200 with an empty body — it surfaces as an
		// unexplained API error with no way to tell what went wrong.
		writeError(w, 502, "api_error", "encode translated response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
}

func (s *Server) streamResponse(w http.ResponseWriter, resp *http.Response, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if err := streamTranslate(w, resp.Body, model); err != nil {
		log.Printf("ultra-zen proxy stream: %v", err)
	}
}

// writeError emits an Anthropic-shaped error response.
func writeError(w http.ResponseWriter, status int, typ, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    typ,
			"message": message,
		},
	}
	out, _ := json.Marshal(body)
	w.Write(out)
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 0} // no overall timeout; streaming can be long
}

// Port returns the configured listen port.
func (s *Server) Port() int { return s.cfg.Port }

// hasInteractiveTools reports whether the tool list includes tools that only
// appear in the main Claude Code loop (AskUserQuestion, Skill, etc.).
// Sub-agents/Workflow workers never see these, so their absence reliably
// identifies a background request that can use the cheaper worker model.
func hasInteractiveTools(tools []anthropicTool) bool {
	for _, t := range tools {
		switch t.Name {
		case "AskUserQuestion", "Skill", "EnterPlanMode", "ExitPlanMode":
			return true
		}
	}
	return false
}
