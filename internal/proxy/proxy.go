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
	"strconv"
	"time"
)

// Config holds the gateway target and credentials for the proxy.
type Config struct {
	BaseURL string // e.g. https://opencode.ai/zen/go/v1
	APIKey string
	Model  string // the Zen model id to forward to
	Port   int    // local listen port
}

// Server is the in-process Anthropic->OpenAI bridge.
type Server struct {
	cfg     Config
	srv     *http.Server
	baseURL string // resolved address, e.g. http://127.0.0.1:8787
}

// New creates a proxy listening on the given port. Call Start to run it.
func New(cfg Config) *Server {
	if cfg.Port == 0 {
		cfg.Port = 8787
	}
	s := &Server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleHealth) // any other path -> health
	s.srv = &http.Server{
		Handler:      mux,
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
	oreq, err := areq.toOpenAI(s.cfg.Model)
	if err != nil {
		writeError(w, 400, "invalid_request_error", err.Error())
		return
	}
	payload, err := json.Marshal(oreq)
	if err != nil {
		writeError(w, 500, "api_error", err.Error())
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		writeError(w, 500, "api_error", err.Error())
		return
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	upstreamReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient().Do(upstreamReq)
	if err != nil {
		writeError(w, 502, "api_error", "gateway request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		ub, _ := io.ReadAll(resp.Body)
		// Pass the gateway error through in Anthropic error shape so Claude
		// Code surfaces it instead of crashing.
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

// PortString returns the port as a string for env vars.
func (s *Server) PortString() string { return strconv.Itoa(s.cfg.Port) }