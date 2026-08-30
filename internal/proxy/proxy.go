package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raketenkater/ultra-zen/internal/models"
)

// Config holds the gateway target and credentials for the proxy.
type Config struct {
	Provider         string // provider name for the primary route
	BaseURL          string // e.g. https://opencode.ai/zen/go/v1
	APIKey           string
	Model            string        // the Zen model id to forward orchestrator requests to
	Kind             string        // primary route wire protocol ("" chat, "responses" for codex-sub)
	AccountID        string        // ChatGPT-Account-ID header for the codex-sub backend
	WorkerModel      string        // if set, background sub-agents use this cheaper model
	Fallbacks        []Upstream    // ordered free-model fallbacks; replaces worker routing when set
	OpenRouterRPM    int           // session-wide request pace for OpenRouter free models
	RateLimitRetries int           // full-pool retries after temporary 429s; zero uses the default
	RateLimitBackoff time.Duration // initial temporary-429 backoff; zero uses the default
	Port             int           // local listen port
	Models           []ModelInfo   // full model list advertised at /v1/models
	Upstreams        []Upstream    // every known upstream route (primary + fallbacks); maps /model ids to gateways
	// AllModels reorganizes /v1/models into per-provider free/paid sub-sections
	// (the --all-models flag). When false, the advertised list is byte-identical
	// to the legacy single-header-per-provider layout.
	AllModels     bool
	ContextLength int            // primary model's context window in tokens (0 = unknown); used to truncate over-limit requests
	OnUnavailable func(Upstream) // called after an explicit per-model access denial
}

// primaryUpstream returns the canonical Upstream for the primary route,
// carrying the wire protocol kind and ChatGPT account id.
func (c Config) primaryUpstream() Upstream {
	return Upstream{Provider: c.Provider, BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model, Kind: c.Kind, AccountID: c.AccountID, ContextLength: c.ContextLength}
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
	// Kind selects the upstream wire protocol. Empty / "" is the default
	// OpenAI Chat Completions path (all existing gateways). "responses" uses
	// the OpenAI Responses API (the ChatGPT subscription backend).
	Kind string
	// AccountID is the ChatGPT-Account-ID header for the codex-sub backend.
	AccountID string
	// ContextLength is the model's context window in tokens (0 = unknown). The
	// proxy uses it to truncate over-limit requests so a session that grew past
	// the wire limit can still make progress instead of hard-failing with a 400.
	ContextLength int
}

// Upstream kinds.
const (
	UpstreamChat      = ""          // OpenAI Chat Completions (default)
	UpstreamResponses = "responses" // OpenAI Responses API (codex-sub)
)

// ModelInfo is a minimal model entry for /v1/models advertising. Provider
// labels the owning provider so main.go can group the list; the /v1/models
// handler renders group headers as real selectable ids (Claude Code's picker
// has no non-selectable separator concept — verified against the installed
// CLI — so a header is a routing-neutral id whose display name carries the
// group title).
type ModelInfo struct {
	ID            string
	Name          string
	Provider      string // provider name, used to insert a group header before it
	ContextLength int    // model context window in tokens; 0 = unknown
	Free          bool   // whether this model is the free tier of its provider
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
	// nextEligible is the per-route cooldown park (pool-indexed, zero = never
	// parked). A temporary 429 / 5xx / transient availability 400 parks the
	// route until the stored time so the next turn does not re-probe a
	// freshly-failed route head-of-line forever; the park expires on its own
	// and a success clears it. Transport errors only rotate the cursor (an
	// unreachable endpoint may be our own egress failing, not the route's).
	// Guarded by poolMu.
	nextEligible []time.Time
	// strikes counts consecutive temporary failures per route (pool-indexed)
	// and drives the exponential cooldown ladder (5m -> 15m -> 60m, capped).
	// Reset on a good 200. Guarded by poolMu.
	strikes []int
	// now is the clock seam for cooldown tests (nil in production = time.Now).
	now func() time.Time
	// deadSelectable remembers /model-selectable routes that are NOT in the
	// rotation pool (full-catalog models) but were hard-denied (403/permanent)
	// this session. They have no pool index, so exhaustedRoute can't mark them;
	// this set stops routeOrder re-trying a dead selectable model first on every
	// subsequent request that re-selects it. Guarded by poolMu.
	deadSelectable map[string]bool
	gateMu         sync.Mutex
	nextOpenRouter time.Time
	// modelRoute maps every id Claude Code's /model command can hand us (both
	// the plain Zen id and the provider-qualified id) to the upstream that
	// serves it. It is built once at New from cfg.Upstreams.
	modelRoute map[string]Upstream
	// usageTracker records per-provider usage exposed at /v1/usage and fed by the
	// request path (rate-limit headers, exhaustion) and the background poller.
	usage *usageTracker
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
		nextEligible:   make([]time.Time, 1+len(cfg.Fallbacks)),
		strikes:        make([]int, 1+len(cfg.Fallbacks)),
		deadSelectable: make(map[string]bool),
		modelRoute:     buildModelRoute(cfg),
		usage:          newUsageTracker(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/usage", s.handleUsage)
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
	// Persist the listen address so `ultra-zen usage` (statusline) can find the
	// running proxy without a shared PID file. Best-effort: a failure to write
	// is logged and otherwise ignored — it must never break the launch.
	s.writeProxyInfo()
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

// writeProxyInfo persists {url, pid, port, startedAt} to
// ~/.cache/ultra-zen/proxy.json (temp file + rename, mirroring the gateway-cache
// atomic-write pattern). Non-fatal on error: the statusline simply falls back to
// reporting "no running proxy" when the file is absent.
func (s *Server) writeProxyInfo() {
	dir, err := os.UserCacheDir()
	if err != nil {
		log.Printf("ultra-zen: proxy info dir unavailable: %v", err)
		return
	}
	dir = filepath.Join(dir, "ultra-zen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("ultra-zen: proxy info dir: %v", err)
		return
	}
	info := map[string]any{
		"url":       s.baseURL,
		"pid":       os.Getpid(),
		"port":      s.cfg.Port,
		"startedAt": time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(info)
	if err != nil {
		log.Printf("ultra-zen: proxy info encode: %v", err)
		return
	}
	path := filepath.Join(dir, "proxy.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("ultra-zen: proxy info write: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("ultra-zen: proxy info rename: %v", err)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

// handleUsage exposes the per-provider usage snapshot at GET /v1/usage for the
// Claude Code statusline (`ultra-zen usage`). It is a sub-millisecond read-locked
// JSON dump; it never touches the request path.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := json.Marshal(map[string]any{
		"schema":    "ultra-zen/usage/v1",
		"server":    map[string]any{"url": s.baseURL},
		"providers": s.usage.getRows(),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
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
	entry := func(id, name string, disabled bool, contextWindow int) map[string]any {
		if name == "" {
			name = id
		}
		e := map[string]any{
			"type":         "model",
			"id":           id,
			"display_name": name,
			"created_at":   "2026-01-01T00:00:00Z",
			"object":       "model",
			"owned_by":     "ultra-zen",
		}
		if contextWindow > 0 {
			// Claude Code reads this to compute its autocompaction threshold. Without
			// it, it guesses the window (often 1M for known models) and compaction
			// never fires before the gateway's real limit — the conversation overflows
			// and /compact fails with a context_length 400.
			e["context_window"] = contextWindow
		}
		if disabled {
			e["disabled"] = true
		}
		return e
	}
	var models []map[string]any
	// Group headers: Claude Code's /model picker has no non-selectable separator
	// (verified against the installed CLI — only "disabled":true and a
	// /(claude|anthropic)/i id filter apply). A header is therefore a real id
	// whose display name names the group, marked disabled so picking it is
	// impossible (it routes nowhere — buildModelRoute never registers it). The
	// "claude" in the id lets it survive the picker's gateway filter.
	headerID := func(provider string) string {
		if provider == "" {
			return ""
		}
		return "claude-group-" + provider
	}
	// When --all-models is set, a provider that exposes BOTH free and paid models
	// gets two sub-headers ("Provider · free" / "Provider · paid") so the picker
	// cleanly separates tiers. Both ids start with "claude-group-" so they
	// survive the gateway filter and render disabled.
	tierHeaderID := func(provider string, free bool) string {
		if provider == "" {
			return ""
		}
		if free {
			return "claude-group-" + provider + "-free"
		}
		return "claude-group-" + provider + "-paid"
	}
	tierHeaderTitle := func(provider string, free bool) string {
		if free {
			return groupTitle(provider) + " · free"
		}
		return groupTitle(provider) + " · paid"
	}
	// ModelTierDisplayName renders the /v1/models display_name for a model when
	// the --all-models layout is active: the friendly name plus the provider
	// label plus a free/paid tier tag, so the picker identifies model, provider,
	// and tier at a glance. Falls back to ModelDisplayName when no tier tag is
	// wanted.
	ModelTierDisplayName := func(name, provider string, free bool) string {
		base := ModelDisplayName(name, provider)
		if free {
			return base + " (free)"
		}
		return base + " (paid)"
	}

	if !s.cfg.AllModels {
		// Legacy layout: one header per provider, models listed beneath it.
		seenProvider := map[string]bool{}
		for _, m := range s.cfg.Models {
			if m.Provider != "" && !seenProvider[m.Provider] {
				seenProvider[m.Provider] = true
				models = append(models, entry(headerID(m.Provider), groupTitle(m.Provider), true, 0))
			}
			advertisedID := m.ID
			if m.Provider != "" {
				advertisedID = ClaudeModelID(m.Provider, m.ID)
			}
			models = append(models, entry(advertisedID, ModelDisplayName(m.Name, m.Provider), false, m.ContextLength))
		}
	} else {
		// --all-models layout: per provider, emit free-then-paid sub-sections.
		// Group models by provider, then by tier, so each provider shows first
		// its free models and then its paid models (each tier sorted by name).
		type grouped struct {
			free []ModelInfo
			paid []ModelInfo
		}
		byProvider := map[string]*grouped{}
		var order []string
		seenProvider := map[string]bool{}
		for _, m := range s.cfg.Models {
			if m.Provider == "" {
				// Models without a provider keep the legacy single-header
				// treatment so we never drop them.
				continue
			}
			g, ok := byProvider[m.Provider]
			if !ok {
				g = &grouped{}
				byProvider[m.Provider] = g
				order = append(order, m.Provider)
				seenProvider[m.Provider] = true
			}
			if m.Free {
				g.free = append(g.free, m)
			} else {
				g.paid = append(g.paid, m)
			}
		}
		// Models without a provider: still render under a single header.
		var orphan []ModelInfo
		for _, m := range s.cfg.Models {
			if m.Provider == "" {
				orphan = append(orphan, m)
			}
		}
		sortProviderGroups := func(infos []ModelInfo) {
			sort.SliceStable(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
		}
		emitProvider := func(provider string) {
			g := byProvider[provider]
			hasFree := len(g.free) > 0
			hasPaid := len(g.paid) > 0
			if hasFree && hasPaid {
				// Two sub-headers: free then paid.
				models = append(models, entry(tierHeaderID(provider, true), tierHeaderTitle(provider, true), true, 0))
				sortProviderGroups(g.free)
				for _, m := range g.free {
					models = append(models, entry(ClaudeModelID(provider, m.ID), ModelTierDisplayName(m.Name, provider, true), false, m.ContextLength))
				}
				models = append(models, entry(tierHeaderID(provider, false), tierHeaderTitle(provider, false), true, 0))
				sortProviderGroups(g.paid)
				for _, m := range g.paid {
					models = append(models, entry(ClaudeModelID(provider, m.ID), ModelTierDisplayName(m.Name, provider, false), false, m.ContextLength))
				}
			} else if hasFree {
				models = append(models, entry(headerID(provider), groupTitle(provider), true, 0))
				sortProviderGroups(g.free)
				for _, m := range g.free {
					models = append(models, entry(ClaudeModelID(provider, m.ID), ModelTierDisplayName(m.Name, provider, true), false, m.ContextLength))
				}
			} else if hasPaid {
				models = append(models, entry(headerID(provider), groupTitle(provider), true, 0))
				sortProviderGroups(g.paid)
				for _, m := range g.paid {
					models = append(models, entry(ClaudeModelID(provider, m.ID), ModelTierDisplayName(m.Name, provider, false), false, m.ContextLength))
				}
			}
		}
		for _, provider := range order {
			emitProvider(provider)
		}
		for _, m := range orphan {
			models = append(models, entry(m.ID, m.Name, false, m.ContextLength))
		}
	}
	if len(models) == 0 {
		models = append(models, entry(s.cfg.Model, "", false, 0))
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

// ClaudeModelID produces the id ultra-zen advertises at /v1/models for a model.
// Claude Code's /model gateway discovery filters advertised ids with
// /(claude|anthropic)/i (verified in the installed binary), so a real model id
// like "deepseek-v4-flash" would be silently dropped. Prefixing every
// advertised id with "claude-" makes the whole catalog survive the filter. The
// proxy's modelRoute maps these advertised ids back to the real upstream model.
func ClaudeModelID(provider, model string) string {
	// Sanitize: strip anything that would make a weird id (slashes, colons).
	clean := strings.NewReplacer("/", "-", ":", "-", " ", "-").Replace(model)
	return "claude-" + provider + "-" + clean
}

// groupTitle renders a provider name as a /v1/models group header title.
func groupTitle(provider string) string {
	switch provider {
	case "opencode-go":
		return "opencode Zen"
	case "openrouter":
		return "OpenRouter"
	case "saia":
		return "GWDG SAIA"
	case "codex":
		return "Codex (ChatGPT sub)"
	case "codex-sub":
		return "Codex (ChatGPT sub)"
	default:
		return provider
	}
}

// ModelDisplayName renders the /v1/models display_name for a model: the
// friendly name plus the provider label, so the picker identifies both the
// model and where it comes from. e.g. "GLM 5.2 — ModelScope".
func ModelDisplayName(name, provider string) string {
	if provider == "" || name == "" {
		return name
	}
	return name + " — " + groupTitle(provider)
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
		// Plain Zen id, the provider-qualified spelling, and the claude-prefixed
		// advertised id (what /v1/models shows and /model sends back) so every
		// form a /model switch might send resolves to the same upstream.
		m[u.Model] = u
		if u.Provider != "" {
			m[u.Provider+"/"+u.Model] = u
			m[ClaudeModelID(u.Provider, u.Model)] = u
		}
	}
	add(cfg.primaryUpstream())
	// Prefer cfg.Upstreams (primary + fallbacks + every selectable model the
	// catalog advertises) so a /model pick of any advertised model routes to the
	// right gateway. Fall back to cfg.Fallbacks for callers (tests) that only set
	// the rotation pool. Re-adding the primary is harmless: map keys overwrite
	// with the identical struct.
	src := cfg.Upstreams
	if len(src) == 0 {
		src = cfg.Fallbacks
	}
	for _, u := range src {
		add(u)
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
		s.reject(w, 400, "invalid_request_error", "could not read request body: "+err.Error())
		return
	}
	var areq anthropicRequest
	if err := json.Unmarshal(body, &areq); err != nil {
		s.reject(w, 400, "invalid_request_error", "invalid Anthropic request: "+err.Error())
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
	primary := s.cfg.primaryUpstream()
	// An explicitly different model id (a /model switch) wins over everything,
	// including the worker split. The primary's own id is not a switch: it is
	// the default the client sends when nothing was picked, so it must fall
	// through to the worker for background sub-agents below.
	if u, ok := s.modelRoute[areq.Model]; ok && u != primary {
		primary = u
	} else if strings.HasPrefix(areq.Model, "claude-group-") {
		// A disabled group header (claude-group-*) is advertised at /v1/models
		// purely as a section label and routes nowhere. If a client still sends
		// one, fail loudly instead of silently serving the primary — a user who
		// picked a section should never silently get a model. Any other
		// unrecognized model id keeps the primary fallback (harmless for
		// arbitrary/other-client requests).
		s.reject(w, 400, "invalid_request_error", fmt.Sprintf("model group %q is a section header, not a selectable model; run /model to pick one", areq.Model))
		return
	} else if (areq.Model == "" || areq.Model == primary.Model) && len(s.cfg.Fallbacks) == 0 && s.cfg.WorkerModel != "" && !hasInteractiveTools(areq.Tools) {
		// Background sub-agent with no explicit /model pick: run on the worker.
		// Only a request naming the primary id (or nothing) is a candidate — any
		// other explicit model id, even one not in the route map, is a deliberate
		// /model choice and must not be overridden by the worker.
		primary.Model = s.cfg.WorkerModel
	}
	oreq, err := areq.toOpenAI(primary.Model)
	if err != nil {
		s.reject(w, 400, "invalid_request_error", err.Error())
		return
	}
	// Clamp max_tokens to a safe ceiling. Claude Code frequently requests very
	// large values that the Zen gateway rejects with a 400.
	if oreq.MaxTokens > maxOutputTokens || oreq.MaxTokens <= 0 {
		oreq.MaxTokens = maxOutputTokens
	}
	payload, resp, used, err := s.forwardWithRateLimit(r.Context(), primary, oreq)
	if err != nil {
		s.reject(w, 502, "api_error", "gateway request failed: "+err.Error())
		return
	}
	if resp == nil {
		// Every pool route was ineligible — either still inside a temporary
		// cooldown park, or permanently exhausted (a spent free allocation).
		// Distinguish them so the user hears an honest reason: a cooldown is a
		// "retry in a few minutes" state, not "out of free models" (which would
		// wrongly tell them the session budget is gone and send them hunting for
		// a new account). Status stays 429 + rate_limit_error so Claude Code
		// retries the turn on its own.
		if parked, soonest := s.coolDownAwait(); parked {
			mins := cooldownMinutes(soonest.Sub(s.clockNow()))
			s.reject(w, 429, "rate_limit_error", fmt.Sprintf("all free routes cooling down, retry in ~%dm", mins))
		} else {
			s.reject(w, 429, "rate_limit_error", "every configured free model is exhausted for this session")
		}
		return
	}
	defer resp.Body.Close()

	// On a 400 "Upstream request failed", retry. Two strategies:
	//   1. Same params (handles transient backend failures).
	//   2. Halve max_tokens (handles oversized-token 400s).
	if resp.StatusCode == http.StatusBadRequest {
		ub, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.dumpFailingRequest(used, ub, payload)
		log.Printf("ultra-zen proxy: upstream 400 (max_tokens=%d): %s | request: %s", oreq.MaxTokens, truncate(string(ub), 200), truncate(string(payload), 500))

		// First retry: same params (handles transient backend failures).
		_, resp2, err := s.forwardTo(r.Context(), used, oreq)
		if err != nil {
			s.reject(w, 502, "api_error", "gateway retry failed: "+err.Error())
			return
		}
		if resp2.StatusCode == http.StatusOK {
			s.recordRetryServed(used, resp2)
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
					s.reject(w, 502, "api_error", "gateway retry failed: "+err.Error())
					return
				}
				if resp3.StatusCode == http.StatusOK {
					s.recordRetryServed(used, resp3)
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
		// The serving route's pool index lets the stream relay park a route that
		// answers with degenerate empty turns. poolIndexOf is -1 for a non-pool
		// selectable/worker route; parking no-ops on -1.
		s.streamResponse(w, r, resp, areq.Model, used.Kind, s.poolIndexOf(used))
		return
	}
	s.nonStreamResponse(w, resp, areq.Model, used.Kind)
}

// routeChoice retains a route's stable pool index so concurrent requests can
// promote a working fallback or retire a model whose free allocation ended.
type routeChoice struct {
	Upstream
	index int
	// explicit marks a route chosen by an authoritative /model selection. Such a
	// route bypasses the exhaustion/denial gate so a deliberate user pick is
	// always attempted first, even if its pool slot was previously exhausted.
	explicit bool
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
	var lastRequest *openAIRequest
	var lastRetryAfter time.Duration
	// lastCallErr records the most recent transport failure so the final return
	// can surface a real 502 instead of the "every route exhausted" 429 when every
	// pool route was unreachable (no HTTP response at all).
	var lastCallErr error

	for round := 0; round <= retries; round++ {
		routes := s.routeOrder(primary)
		if len(routes) == 0 {
			break
		}
		temporary := false
		// sawThrottle tracks whether at least one route answered with a
		// throttle-class HTTP response this round. Transport errors and a dead
		// context set `temporary` (the pool must be retried) but are NOT
		// throttles; without this the "every available route is throttled" line
		// lied after a Claude Code cancel, which is how #3 echoed a fake 429
		// back at the user.
		sawThrottle := false
		for _, choice := range routes {
			// A dead context is not a route condition: return it before
			// touching the pool so a cancel can never reorder routes, park
			// anyone, or emit a throttle log.
			if err := ctx.Err(); err != nil {
				return nil, nil, Upstream{}, err
			}
			if !choice.explicit && s.routeExhausted(choice.index) {
				continue
			}
			if err := s.waitOpenRouterSlot(ctx, choice.BaseURL); err != nil {
				return nil, nil, Upstream{}, err
			}
			// Fit a private copy to the route actually being attempted. Pool models
			// can have different context windows; mutating the shared request for a
			// small fallback would needlessly damage the prompt later sent to a
			// larger route.
			routeReq := oreq.clone()
			routeReq.Model = choice.Model
			if note := routeReq.truncateToContext(choice.ContextLength, routeReq.MaxTokens); note != "" {
				log.Printf("ultra-zen proxy: %s for %s", note, choice.Model)
			}
			p, candidate, callErr := s.forwardTo(ctx, choice.Upstream, routeReq)
			if callErr != nil {
				// A canceled request (Claude Code interrupted the turn, or the
				// proxy's context is otherwise done) surfaces as a transport
				// error on whatever route was in flight. That is not the
				// route's fault: reordering or parking it on every cancel was
				// mechanism #3's echo, and the resulting backoff log told the
				// user "every available route is throttled" about a request
				// nobody was making any more. Return the error untouched.
				if errors.Is(callErr, context.Canceled) {
					if candidate != nil {
						candidate.Body.Close()
					}
					return nil, nil, Upstream{}, callErr
				}
				// A transport failure (dial timeout, connection refused, TLS error)
				// is a temporary endpoint outage, not a permanent request error.
				// Soft-skip to the next pool route instead of surfacing a 502
				// straight to Claude Code; if every route is down the outer loop
				// retries with backoff and the last error is returned.
				s.limitRoute(choice.index, false)
				lastPayload, lastBody, lastResp, lastUsed, lastRequest = p, nil, nil, choice.Upstream, routeReq
				lastCallErr = callErr
				temporary = true
				log.Printf("ultra-zen proxy: transport error on %s: %v; rotating", choice.Model, callErr)
				continue
			}
			// servedOK marks a 200 whose body classified as a real completion.
			// Only such responses get the pool-promotion and usage bookkeeping
			// in the return branch below: the 2xx-with-error-body and
			// degenerate cases rotate with `continue` before reaching it (they
			// never promoteRoute), and any remaining non-200 pass-through is a
			// genuine client error that must not reorder the pool either.
			servedOK := false
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
						s.retireRoute(choice)
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
					// An empty HTTP-200 response is commonly a transient gateway/backend
					// failure. Permanently retiring the selected model poisoned the rest
					// of a long-running proxy process, while restarting ultra-zen made
					// the exact same model and Claude session work again. Rotate for this
					// request but retry the selected route on the next turn. Explicit
					// access denials and exhausted allocations remain permanent below.
					s.limitRoute(choice.index, false)
					log.Printf("ultra-zen proxy: empty completion from %s; temporarily rotating (%s)", choice.Model, truncate(string(prefix), 200))
					candidate.Body.Close()
					continue
				}
				servedOK = true
			}
			if candidate.StatusCode == http.StatusForbidden {
				body, _ := io.ReadAll(candidate.Body)
				candidate.Body.Close()
				candidate.Body = io.NopCloser(bytes.NewReader(body))
				if isModelAccessDenied(body) {
					s.retireRoute(choice)
					if s.cfg.OnUnavailable != nil {
						s.cfg.OnUnavailable(choice.Upstream)
					}
					lastPayload, lastBody, lastResp, lastUsed = p, body, candidate, choice.Upstream
					log.Printf("ultra-zen proxy: model unavailable for this account: %s; retiring route (%s)", choice.Model, truncate(string(body), 200))
					continue
				}
			}
			// A 5xx from the upstream (503 "Endpoint is unavailable", 500 Internal
			// server error) is a temporary endpoint outage, not a permanent request
			// error. Soft-skip to the next pool route (limitRoute(false) rotates the
			// cursor but leaves the route eligible), mark the round temporary so the
			// outer backoff loop retries the whole pool, and DON'T promoteRoute the
			// broken route (promoting it would make every later request start there).
			if candidate.StatusCode == http.StatusServiceUnavailable ||
				candidate.StatusCode == http.StatusInternalServerError ||
				candidate.StatusCode == http.StatusBadGateway ||
				candidate.StatusCode == http.StatusGatewayTimeout {
				body, _ := io.ReadAll(candidate.Body)
				candidate.Body.Close()
				candidate.Body = io.NopCloser(bytes.NewReader(body))
				s.limitRoute(choice.index, false)
				s.parkRoute(choice.index)
				temporary = true
				lastPayload, lastBody, lastResp, lastUsed = p, body, candidate, choice.Upstream
				log.Printf("ultra-zen proxy: upstream %d (endpoint unavailable) on %s: %s; rotating", candidate.StatusCode, choice.Model, truncate(string(body), 200))
				continue
			}
			// A 400 can be a transient availability failure in disguise: the
			// opencode Zen gateway answers "Upstream request failed: Model is
			// unavailable." with HTTP 400 inside a server_error envelope. That
			// is the same class as a 5xx outage — soft-rotate to the next pool
			// route (limitRoute + park, explicitly WITHOUT promoteRoute, which
			// pinned the dead route head-of-line) instead of ending the turn.
			// Request-shaped 400s (invalid_request_error with a param, context
			// length, data_inspection_failed) do not match the predicate and
			// fall through to the return branch below so handleMessages'
			// halving retry keeps fixing them — rotating those would hide a
			// request bug.
			if candidate.StatusCode == http.StatusBadRequest {
				body, _ := io.ReadAll(candidate.Body)
				candidate.Body.Close()
				candidate.Body = io.NopCloser(bytes.NewReader(body))
				if isTransientUpstreamFailure(body) {
					s.limitRoute(choice.index, false)
					s.parkRoute(choice.index)
					temporary = true
					lastPayload, lastBody, lastResp, lastUsed = p, body, candidate, choice.Upstream
					log.Printf("ultra-zen proxy: transient availability 400 on %s: %s; rotating", choice.Model, truncate(string(body), 200))
					continue
				}
			}
			if candidate.StatusCode != http.StatusTooManyRequests {
				// Only a 200 whose body classified as a real completion counts
				// as success: promote the route and book the usage. Anything
				// else reaching this branch is a pass-through client error the
				// caller must still see (e.g. a request-shaped 400 for the
				// halving retry) — promoting it would pin a failing route at
				// the head of every later rotation. The 2xx-with-error-body
				// and degenerate cases already `continue`d above, so servedOK
				// is exactly StatusCode==200 && bodyOK.
				if servedOK {
					s.promoteRoute(choice.index)
					s.clearRouteCooldown(choice.index)
					// Success: clear any prior exhaustion so a provider that came
					// back online stops showing as "hit". The served-request counter
					// and the :free quota tally below only track real completions — a
					// 400 (bad params) still gets handed through to the caller's
					// retry path, but it is not a served request and OpenRouter does
					// not meter it against the free cap.
					s.usage.setExhausted(choice.Upstream.Provider, false)
					s.usage.recordRequest(choice.Upstream.Provider)
					// OpenRouter meters :free models (and the openrouter/free
					// router) against a per-UTC-day request cap with no readable
					// API for ordinary keys, so count locally for the statusline.
					if choice.Upstream.Provider == "openrouter" && models.OpenRouterFreeModel(choice.Upstream.Model) {
						models.RecordORFreeRequest()
					}
				}
				*oreq = *routeReq
				return p, candidate, choice.Upstream, nil
			}

			body, _ := io.ReadAll(candidate.Body)
			candidate.Body.Close()
			candidate.Body = io.NopCloser(bytes.NewReader(body))
			accountExhausted := isOpenRouterDailyLimit(body)
			providerExhausted := accountExhausted || isFreeUsageLimit(body)
			sawThrottle = true
			if providerExhausted {
				s.exhaustProviderRoutes(choice.Upstream)
				// Mark the provider exhausted for the statusline. SAIA and other
				// free tiers that should NOT permanently retire still get cleared
				// on the next good 200 (setExhausted(...,false)).
				s.usage.setExhausted(choice.Upstream.Provider, true)
			} else {
				s.limitRoute(choice.index, false)
				// A temporary 429 parks the route for a cooldown window so the
				// next turn starts elsewhere instead of re-probing the throttled
				// route first every few seconds (the glm-5.2:free 131-hits bug).
				s.parkRoute(choice.index)
			}
			// Capture any rate-limit response headers for the provider so the
			// statusline can show remaining-request counts even for providers
			// without a live usage endpoint.
			s.usage.recordRateLimit(choice.Upstream.Provider, candidate.Header)
			if !providerExhausted {
				temporary = true
			}
			lastRetryAfter = parseRetryAfter(candidate.Header.Get("Retry-After"))
			lastPayload, lastBody, lastResp, lastUsed, lastRequest = p, body, candidate, choice.Upstream, routeReq
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
			log.Printf("ultra-zen proxy: backoff %s exceeds the %s ceiling; giving up on this round's retry loop", delay, maxRateLimitWait)
			break
		}
		// Only say "throttled" when a route actually answered with a throttle
		// class this round; a round that failed purely on transport errors is an
		// outage, and calling it a throttle sent users chasing rate limits that
		// never existed.
		if sawThrottle {
			log.Printf("ultra-zen proxy: every available route is throttled; retrying in %s", delay)
		} else {
			log.Printf("ultra-zen proxy: every available route failed transiently; retrying in %s", delay)
		}
		if err := sleepContext(ctx, delay); err != nil {
			return nil, nil, Upstream{}, err
		}
	}

	if lastResp != nil {
		lastResp.Body = io.NopCloser(bytes.NewReader(lastBody))
	}
	if lastRequest != nil {
		*oreq = *lastRequest
	}
	// Every route was unreachable (no HTTP response at all) — surface the actual
	// transport error so handleMessages reports a 502, not a bogus "429 all models
	// exhausted". A transient 503/500 (which produced a real response) still
	// returns the last upstream body through lastResp above.
	if lastResp == nil && lastCallErr != nil {
		return nil, nil, Upstream{}, lastCallErr
	}
	return lastPayload, lastResp, lastUsed, nil
}

// recordRetryServed performs the success accounting for the 400-retry path in
// handleMessages, mirroring the in-loop bookkeeping in forwardWithRateLimit.
// Like the main loop, the accounting only runs when the 200's body
// classifies as a real completion: several gateways serve error objects or
// empty choices with HTTP 200, and the main path rotates past those without
// counting them — a retry round must not bump the :free tally for a body the
// main path would reject. The peeked prefix is rewound, so the caller still
// receives the full response. The failed 400 attempt itself was never
// metered by OpenRouter (a rejected request does not spend a :free slot), so
// a gated-in retry 200 counts exactly once — no double-counting across the
// original round and the retry.
func (s *Server) recordRetryServed(u Upstream, resp *http.Response) {
	prefix := make([]byte, 64*1024)
	n, _ := io.ReadFull(resp.Body, prefix)
	prefix = prefix[:n]
	resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), resp.Body))
	if classifyUpstreamBody(prefix) != bodyOK {
		return
	}
	// Aligned with the main loop's success branch: a proven-good 200 promotes
	// the route, clears any cooldown, and books the usage.
	idx := s.poolIndexOf(u)
	s.promoteRoute(idx)
	s.clearRouteCooldown(idx)
	s.usage.recordRequest(u.Provider)
	s.usage.setExhausted(u.Provider, false)
	if u.Provider == "openrouter" && models.OpenRouterFreeModel(u.Model) {
		models.RecordORFreeRequest()
	}
}

// poolIndexOf resolves an upstream back to its rotation-pool index, or -1 for
// a route outside the pool (the legacy no-fallback primary, a worker model, an
// index -1 selectable). promoteRoute/clearRouteCooldown no-op on -1.
func (s *Server) poolIndexOf(u Upstream) int {
	if !sameUpstream(u, s.cfg.primaryUpstream()) && len(s.cfg.Fallbacks) > 0 {
		for i, f := range s.cfg.Fallbacks {
			if sameUpstream(u, f) {
				return 1 + i
			}
		}
		return -1
	}
	if sameUpstream(u, s.cfg.primaryUpstream()) {
		return 0
	}
	return -1
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
	pool = append(pool, entry{s.cfg.primaryUpstream(), 0})
	for i, f := range s.cfg.Fallbacks {
		pool = append(pool, entry{f, 1 + i})
	}

	s.poolMu.Lock()
	defer s.poolMu.Unlock()

	out := make([]routeChoice, 0, len(pool))
	seen := make(map[int]bool, len(pool))
	// An explicit /model selection (primary != the configured launch model) is
	// authoritative and must be attempted first, even if its pool route was
	// previously marked exhausted (FreeUsageLimitError opened the provider
	// circuit) or denied (403). Auto-rotation exhaustion governs only the
	// automatic pool and must never override a deliberate user pick.
	explicit := !sameUpstream(primary, s.cfg.primaryUpstream())

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
	if explicit {
		if primIdx >= 0 {
			// Attempt the explicit pool route FIRST even if
			// exhaustedRoute[primIdx] is true — the user deliberately picked it.
			out = append(out, routeChoice{Upstream: primary, index: primIdx, explicit: true})
			seen[primIdx] = true
		} else {
			// A full-catalog model outside the pool: attempt once, bypassing
			// selectableDead. index -1 is safe: limitRoute/promoteRoute no-op on
			// negatives and routeExhausted(-1) is false, so it is tried once and
			// then the pool rotates.
			out = append(out, routeChoice{Upstream: primary, index: -1, explicit: true})
		}
	} else {
		if primIdx >= 0 && s.routeEligibleLocked(primIdx) {
			out = append(out, routeChoice{Upstream: primary, index: primIdx})
			seen[primIdx] = true
		} else if primIdx < 0 {
			// The /model-selected route is a full-catalog model that is NOT in the
			// rotation pool (its provider was loaded for advertising but the pool was
			// capped). Attempt it first anyway — index -1 is safe: limitRoute and
			// promoteRoute no-op on negatives and routeExhausted(-1) is false, so it
			// is tried once and then the pool rotates. A route permanently retired
			// this session (retireRoute) is skipped so it is not re-tried first on
			// every request.
			if !s.selectableDead(primary) {
				out = append(out, routeChoice{Upstream: primary, index: -1})
			}
		}
	}
	for offset := 0; offset < len(pool); offset++ {
		idx := (s.activeRoute + offset) % len(pool)
		if seen[idx] {
			continue
		}
		if s.routeEligibleLocked(idx) {
			out = append(out, routeChoice{Upstream: pool[idx].u, index: idx})
		}
	}
	return out
}

// routeEligibleLocked reports whether a pool route may be attempted now: not
// permanently exhausted and not inside a temporary-failure cooldown park.
// Callers must hold poolMu.
func (s *Server) routeEligibleLocked(index int) bool {
	if s.exhaustedRoute[index] {
		return false
	}
	return !s.clockNow().Before(s.nextEligible[index])
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

// parkRoute puts a route on a temporary-failure cooldown: one more strike
// extends the park, following the repeat-offense ladder below. This is what
// stops a 429/5xx/transient-400 route from being re-probed head-of-line on
// every turn forever (the glm-5.2:free 131-hits bug).
func (s *Server) parkRoute(index int) {
	if index < 0 || index >= len(s.nextEligible) {
		return
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	s.strikes[index]++
	s.nextEligible[index] = s.clockNow().Add(routeCooldownTTL(s.strikes[index]))
}

// routeCooldownTTL is the repeat-offense ladder: the 1st temporary failure
// parks the route for 5 minutes, the 2nd for 15, the 3rd and every one after
// for 60 (capped). Counting only offenses keeps the ladder deterministic; a
// good 200 clears the count so the next incident starts at 5 minutes.
func routeCooldownTTL(offenses int) time.Duration {
	switch {
	case offenses <= 1:
		return 5 * time.Minute
	case offenses == 2:
		return 15 * time.Minute
	default:
		return 60 * time.Minute
	}
}

// clearRouteCooldown forgets a route's temporary-failure history (strikes and
// park). Called from promoteRoute on a good 200: the route proved healthy, so
// a future failure must start a fresh 5-minute park, not an escalated one.
func (s *Server) clearRouteCooldown(index int) {
	if index < 0 || index >= len(s.strikes) {
		return
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	s.strikes[index] = 0
	s.nextEligible[index] = time.Time{}
}

// clockNow is the cooldown clock seam: s.now when set (tests), time.Now
// otherwise.
func (s *Server) clockNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// coolDownAwait reports whether at least one pool route is currently inside a
// temporary cooldown park (nextEligible in the future), and the soonest such
// park's expiry. When every route is ineligible and nothing responded, this
// distinguishes an all-parked (cooling-down) pool from an all-permanently-
// exhausted one, so handleMessages can emit an honest "retry in a few minutes"
// instead of the misleading "every free model is exhausted". A paused clock or
// an empty/failed park leaves parked=false.
func (s *Server) coolDownAwait() (parked bool, soonest time.Time) {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	now := s.clockNow()
	for i := range s.nextEligible {
		if now.Before(s.nextEligible[i]) {
			if soonest.IsZero() || s.nextEligible[i].Before(soonest) {
				soonest = s.nextEligible[i]
			}
		}
	}
	return !soonest.IsZero(), soonest
}

// cooldownMinutes rounds an unexpired cooldown wait to a whole minute for the
// "retry in ~Nm" message. It never reports 0m (a route cooling down is still
// cooling down), and any sub-minute or already-expired wait reads as ~1m so the
// message always names a positive window.
func cooldownMinutes(wait time.Duration) int {
	if wait < 0 {
		wait = 0
	}
	mins := int(wait.Round(time.Minute) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	return mins
}

func (s *Server) routeExhausted(index int) bool {
	if index < 0 || index >= len(s.exhaustedRoute) {
		return false
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if s.exhaustedRoute[index] {
		return true
	}
	// Permanent retirement (403 denial / FreeUsageLimitError) stays as before.
	// Additionally a route parked in a temporary-failure cooldown reads as not
	// eligible until the park expires on its own — routeExhausted is now
	// time-aware (fix #4/#7).
	if index < len(s.nextEligible) && s.clockNow().Before(s.nextEligible[index]) {
		return true
	}
	return false
}

// retireRoute permanently retires a failed route. Pool routes (index >= 0) are
// marked in exhaustedRoute as usual; a selectable route that is NOT in the
// rotation pool (index -1) has no pool slot, so it is recorded in
// deadSelectable so routeOrder stops re-trying it first this session.
func (s *Server) retireRoute(choice routeChoice) {
	s.limitRoute(choice.index, true)
	if choice.index < 0 {
		s.markSelectableDead(choice.Upstream)
	}
}

// selectableDead reports whether a non-pool selectable route was permanently
// retired this session. Callers must hold poolMu (routeOrder already does).
func (s *Server) selectableDead(u Upstream) bool {
	return s.deadSelectable[selectableDeadKey(u)]
}

// markSelectableDead records a non-pool selectable route as permanently dead
// for this session.
func (s *Server) markSelectableDead(u Upstream) {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	s.deadSelectable[selectableDeadKey(u)] = true
}

// selectableDeadKey uniquely identifies one selectable upstream for the dead
// set. baseURL+model is sufficient because the proxy routes a given model id to
// exactly one selectable upstream per base; including base guards against two
// providers sharing a model id. APIKey is included too so the key matches
// sameUpstream's equality (two routes sharing base+model but distinct keys stay
// independent).
func selectableDeadKey(u Upstream) string {
	return u.BaseURL + "\x00" + u.APIKey + "\x00" + u.Model
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
	routes = append(routes, s.cfg.primaryUpstream())
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

// isTransientUpstreamFailure reports a 400 body that is really an availability
// failure wearing a client-error status code — today's instance is zen-go's
// {"type":"server_error","message":"Error from provider (Console): Upstream
// request failed: Model is unavailable."}. Only this class rotates to another
// pool route: a 400 that names a bad param (invalid_request_error), a context
// length, or moderation stays on the same upstream so handleMessages' halving
// retry can fix it.
//
// The body is parsed structurally first. A lowercased substring scan over the
// raw bytes is too fragile: a pretty-printed body ("type" : "server_error",
// spaces around the colon) missed the literal `"type":"server_error"` and fell
// through to the halving retry on a dead route — exactly the mismatch the live
// gateway produced ("transient availability 400" logged against a route that
// answered "Model is unavailable."). Structural parsing also lets a recognized
// NON-server type (invalid_request_error, context_length, data_inspection_failed)
// short-circuit to false even when its message happens to wrap an
// "Upstream request failed" provider note — those must reach the halving
// retry, never rotate.
func isTransientUpstreamFailure(body []byte) bool {
	var payload struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		typ, msg := payload.Type, payload.Message
		if payload.Error != nil {
			if payload.Error.Type != "" {
				typ = payload.Error.Type
			}
			if payload.Error.Message != "" {
				msg = payload.Error.Message
			}
		}
		// A parsed body that names a recognized NON-server error type is a
		// genuine request-shaped 400: it belongs to the halving retry, even if
		// its message wraps an upstream/provider note.
		if typ != "" && !strings.EqualFold(strings.TrimSpace(typ), "server_error") {
			return false
		}
		if strings.EqualFold(strings.TrimSpace(typ), "server_error") {
			return true
		}
		// No explicit (or an unrecognized) type: fall back to the message
		// heuristic. Only "model is unavailable" counts here — it is a strong,
		// specific availability signal with no request-shaped reading. The
		// generic wrapper "upstream request failed" is deliberately NOT a
		// standalone trigger: many gateways prefix every error with it, so a
		// bare {"error":{"message":"Upstream request failed"}} (no type) is a
		// request-shaped 400 that must still reach the halving retry (pinned by
		// TestRetryServedAppliesBodyGate). "upstream request failed" therefore
		// rotates only when a server_error type confirms it, via the branch
		// above.
		lower := strings.ToLower(strings.TrimSpace(msg))
		return strings.Contains(lower, "model is unavailable")
	}
	// Non-JSON fallback: keep the old lowercased substring scan. Here
	// "upstream request failed" is a generic wrapper, so it must co-occur with
	// a server_error type to count.
	msg := strings.ToLower(string(body))
	if strings.Contains(msg, "model is unavailable") {
		return true
	}
	return strings.Contains(msg, "upstream request failed") &&
		strings.Contains(msg, `"type":"server_error"`)
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
// returning the marshalled payload and raw upstream response. The Responses-API
// kind (codex-sub) is translated to the Responses wire format and posted to
// {base}/responses instead of chat/completions.
func (s *Server) forwardTo(ctx context.Context, upstream Upstream, oreq *openAIRequest) (payload []byte, resp *http.Response, err error) {
	if upstream.Kind == UpstreamResponses {
		payload, err = toResponses(oreq)
		if err != nil {
			return nil, nil, fmt.Errorf("translate to responses: %w", err)
		}
		resp, err = sendResponses(ctx, upstream, payload)
		return payload, resp, err
	}
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

// reject logs and emits an Anthropic-shaped error response. It exists so
// every writeError exit in handleMessages is observable: previously a session
// could end on an error the proxy logged nowhere, leaving only the client's
// redacted UI as evidence.
func (s *Server) reject(w http.ResponseWriter, status int, typ, message string) {
	log.Printf("ultra-zen proxy: request error %d %s: %s", status, typ, message)
	writeError(w, status, typ, message)
}

// dumpFailingRequest writes the full request payload + upstream error to
// ~/.cache/ultra-zen/last-400.json so the exact failing request can be
// inspected without truncation. Overwrites on each 400. The route fields are
// stamped from the upstream that ACTUALLY served the failing request, not the
// launch config: with a fallback pool the dump used to name the primary model
// even when a completely different gateway 400'd, which misdirected triage.
func (s *Server) dumpFailingRequest(used Upstream, upstreamErr []byte, payload []byte) {
	dir := os.Getenv("HOME") + "/.cache/ultra-zen"
	// 0700/0600: the dump holds the full request payload — the entire
	// conversation, including any file contents Claude Code read into context.
	// World-readable modes would expose it to every local user, which matters
	// on the shared-key multi-user setup this tool supports.
	_ = os.MkdirAll(dir, 0700)
	dump := struct {
		Model       string          `json:"model"`
		Upstream    string          `json:"upstream"`
		MaxTokens   int             `json:"max_tokens"`
		Request     json.RawMessage `json:"request"`
		UpstreamErr json.RawMessage `json:"upstream_error"`
	}{
		Model:       used.Model,
		Upstream:    used.BaseURL,
		Request:     json.RawMessage(payload),
		UpstreamErr: json.RawMessage(upstreamErr),
	}
	var p struct {
		MaxTokens int `json:"max_tokens"`
	}
	_ = json.Unmarshal(payload, &p)
	dump.MaxTokens = p.MaxTokens
	b, _ := json.MarshalIndent(dump, "", "  ")
	_ = os.WriteFile(dir+"/last-400.json", b, 0600)
}

// nonStreamResponse converts a non-streaming upstream response to Anthropic.
// Claude Code always requests streaming, so this path is rarely exercised; for
// the Responses kind we read the buffered SSE (the codex backend always streams
// even for a non-stream caller), replay the translated chat chunks into a
// synthetic openAIResponse, and translate that.
func (s *Server) nonStreamResponse(w http.ResponseWriter, resp *http.Response, model, kind string) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.reject(w, 502, "api_error", "read gateway response: "+err.Error())
		return
	}
	if kind == UpstreamResponses {
		// Replay the Responses SSE into chat-completions chunks and fold them
		// into an openAIResponse.
		chunks := collectChatChunks(body)
		if len(chunks) == 0 {
			s.reject(w, 502, "api_error", "empty responses stream from codex backend")
			return
		}
		oresp := chunksToOpenAIResponse(chunks)
		anthropic := oresp.toAnthropic(model)
		out, err := json.Marshal(anthropic)
		if err != nil {
			s.reject(w, 502, "api_error", "encode translated response: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(out)
		return
	}
	var oresp openAIResponse
	if err := json.Unmarshal(body, &oresp); err != nil {
		s.reject(w, 502, "api_error", "parse gateway response: "+err.Error())
		return
	}
	anthropic := oresp.toAnthropic(model)
	out, err := json.Marshal(anthropic)
	if err != nil {
		// Never hand Claude Code a 200 with an empty body — it surfaces as an
		// unexplained API error with no way to tell what went wrong.
		s.reject(w, 502, "api_error", "encode translated response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
}

// collectChatChunks replays a buffered Responses SSE body into chat-completions
// chunks, in order.
func collectChatChunks(body []byte) []chatChunk {
	var chunks []chatChunk
	sc := newScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if cs, ok := responsesEventToChatChunks(payload); ok {
			chunks = append(chunks, cs...)
		}
	}
	return chunks
}

// chunksToOpenAIResponse folds translated chat-completions chunks into a
// non-streaming openAIResponse for translation. Tool calls arrive whole in the
// output_item.done chunk; text accumulates across delta chunks.
func chunksToOpenAIResponse(chunks []chatChunk) openAIResponse {
	var resp openAIResponse
	var text strings.Builder
	var usage *chatUsage
	for _, c := range chunks {
		if resp.ID == "" {
			resp.ID = c.ID
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		for _, ch := range c.Choices {
			if ch.Delta.Content != "" {
				text.WriteString(ch.Delta.Content)
			}
			for _, tc := range ch.Delta.ToolCalls {
				choice := openAIChoice{Index: ch.Index, FinishReason: "tool_calls"}
				choice.Message.Role = "assistant"
				choice.Message.ToolCalls = append(choice.Message.ToolCalls, openAITool{
					ID:   tc.ID,
					Type: "function",
					Function: openAIToolFunc{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
				resp.Choices = append(resp.Choices, choice)
			}
		}
	}
	if text.Len() > 0 && len(resp.Choices) == 0 {
		choice := openAIChoice{Index: 0, FinishReason: "stop"}
		choice.Message.Role = "assistant"
		choice.Message.Content = text.String()
		resp.Choices = append(resp.Choices, choice)
	}
	if usage != nil {
		resp.Usage.PromptTokens = usage.PromptTokens
		resp.Usage.CompletionTokens = usage.CompletionTokens
	}
	return resp
}

// streamResponse streams the upstream response to the client. For the
// Responses-API kind the upstream body is a Responses SSE stream that is first
// translated into chat-completions chunks, which relayStream then converts to
// the Anthropic SSE stream Claude Code reads. routeIdx is the serving route's
// pool index (-1 for a non-pool selectable/worker route); it is parked on a
// degenerate empty turn so the next turn starts on a healthier route.
func (s *Server) streamResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, model, kind string, routeIdx int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	var body io.Reader = resp.Body
	if kind == UpstreamResponses {
		body = responsesSSEStream(r.Context(), resp.Body)
	}
	st, err := relayStream(w, body, model)
	if err != nil {
		// Only the protocol-complete empty-turn failure costs the serving route
		// a cooldown park: the live text_blocks=1 / output_tokens=0 / end_turn
		// stop where the gateway surfaced a complete but content-free turn. A
		// relay that died AFTER real content reached the user already served
		// tokens, and a relay that died before any content (premature EOF,
		// connection drop, stall) is a transport condition, not the route
		// serving empty turns — neither gets a park, just the log line.
		if errors.Is(err, errEmptyTurn) {
			s.parkRoute(routeIdx)
			log.Printf("ultra-zen proxy stream: empty turn from %s (text_blocks=%d tool_blocks=%d output_tokens=%d); parking route %d",
				model, st.textBlocks, len(st.toolStarted), st.output, routeIdx)
		} else {
			log.Printf("ultra-zen proxy stream: %v", err)
		}
		return
	}
	// A genuine completion proves the route works: clear any prior empty-turn
	// park so a future failure starts a fresh 5-minute cooldown, not an
	// escalated one. The main forward loop already cleared the cooldown on the
	// 200, so this is a redundant-but-harmless safety net for the stream path.
	s.clearRouteCooldown(routeIdx)
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
