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
	"strings"
	"time"
)

// Config holds the gateway target and credentials for the proxy.
type Config struct {
	BaseURL     string // e.g. https://opencode.ai/zen/go/v1
	APIKey      string
	Model       string // the Zen model id to forward orchestrator requests to
	WorkerModel string // if set, background sub-agents use this cheaper model
	Port        int    // local listen port
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
	cfg     Config
	srv     *http.Server
	baseURL string // resolved address, e.g. http://127.0.0.1:38271
}

// New creates a proxy listening on the given port. Call Start to run it.
// A Port of 0 lets the OS assign a free port, which allows many ultra-zen
// instances to run concurrently without port collisions.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
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

// handleModels advertises the available model(s) at GET /v1/models. Claude
// Code (and every subagent / background agent it spawns) probes this endpoint
// to validate ANTHROPIC_MODEL before issuing a request. When a worker model is
// configured, both orchestrator and worker are advertised so Claude Code's
// /model command can switch between them at runtime. The proxy still overrides
// the model on every /v1/messages request based on tool classification, so
// listing both ids is correct — the runtime routing is independent of which
// model Claude Code thinks it's using.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	models := []map[string]any{
		{"id": s.cfg.Model, "object": "model", "owned_by": "ultra-zen"},
	}
	if s.cfg.WorkerModel != "" && s.cfg.WorkerModel != s.cfg.Model {
		models = append(models, map[string]any{
			"id": s.cfg.WorkerModel, "object": "model", "owned_by": "ultra-zen",
		})
	}
	out := map[string]any{"object": "list", "data": models}
	body, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
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
	// Model routing: when a worker model is configured, background sub-agents
	// use the cheaper worker model. The main loop carries interactive-only tools
	// (AskUserQuestion, Skill) that sub-agents never see, so we inspect the tool
	// list to classify each request. On local hardware all models cost the same
	// (your electricity), so splitting makes no difference — WorkerModel stays
	// empty and everything uses Model.
	model := s.cfg.Model
	if s.cfg.WorkerModel != "" && !hasInteractiveTools(areq.Tools) {
		model = s.cfg.WorkerModel
	}
	oreq, err := areq.toOpenAI(model)
	if err != nil {
		writeError(w, 400, "invalid_request_error", err.Error())
		return
	}
	// Clamp max_tokens to a safe ceiling. Claude Code frequently requests very
	// large values that the Zen gateway rejects with a 400.
	if oreq.MaxTokens > maxOutputTokens || oreq.MaxTokens <= 0 {
		oreq.MaxTokens = maxOutputTokens
	}

	payload, resp, err := s.forward(r.Context(), oreq)
	if err != nil {
		writeError(w, 502, "api_error", "gateway request failed: "+err.Error())
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
		_, resp2, err := s.forward(r.Context(), oreq)
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
				payload3, resp3, err := s.forward(r.Context(), oreq)
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

// forward marshals the OpenAI request and sends it to the Zen gateway,
// returning the marshalled payload (for diagnostic logging on errors) and
// the raw upstream response.
func (s *Server) forward(ctx context.Context, oreq *openAIRequest) (payload []byte, resp *http.Response, err error) {
	payload, err = json.Marshal(oreq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
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
	out, _ := json.Marshal(anthropic)
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
