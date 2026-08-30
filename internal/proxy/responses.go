// Package proxy — responses.go adapts the OpenAI Responses API (which the
// ChatGPT subscription backend at chatgpt.com/backend-api/codex serves) to the
// Chat Completions wire format the rest of the proxy already translates to and
// from Anthropic. The proxy keeps talking one internal language — OpenAI Chat
// Completions — and this file bridges that language to the Responses API on the
// upstream side, so every hardened Anthropic<->chat-completions translation in
// request.go/response.go/stream.go applies unchanged to ChatGPT models.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Responses base path is joined to the upstream BaseURL (CodexSubBase). The
// backend serves inference at {base}/responses and does not offer
// chat/completions.
const responsesPath = "/responses"

// ---------------------------------------------------------------------------
// Request translation: openAIRequest (chat completions) -> Responses API body.
// ---------------------------------------------------------------------------

// responsesRequest is the subset of the Responses API request body ultra-zen
// needs. It mirrors what llm-openai-via-codex sends to the same backend and is
// deliberately minimal: the codex backend rejects unknown fields (e.g.
// store:true), so anything we do not use is left out.
type responsesRequest struct {
	Model        string            `json:"model"`
	Input        []json.RawMessage `json:"input"`
	Instructions string            `json:"instructions,omitempty"`
	Tools        []json.RawMessage `json:"tools,omitempty"`
	Store        bool              `json:"store"` // always false; backend rejects true
	Stream       bool              `json:"stream"`
	Temperature  *float64          `json:"temperature,omitempty"`
	Reasoning    *responsesReason  `json:"reasoning,omitempty"`
}

type responsesReason struct {
	Effort string `json:"effort,omitempty"`
}

// toResponses translates a chat-completions request (already produced from
// Anthropic by anthropicRequest.toOpenAI) into a Responses API request body.
//
// Chat-completions message roles map to Responses input items:
//
//	system        -> instructions (joined, single string)
//	user          -> {"role":"user","content":[...]}
//	assistant     -> {"role":"assistant","content":[...]}
//	tool          -> {"type":"function_call_output","call_id":...,"output":...}
//
// Assistant tool_calls map to a single {"type":"function_call","call_id","name",
// "arguments","role":"assistant"} item. Responses API requires function_calls to
// be grouped after the assistant text (they are not separate messages), and
// function_call_output items must reference a call_id the backend has seen.
func toResponses(req *openAIRequest) ([]byte, error) {
	body := responsesRequest{
		Model:        req.Model,
		Instructions: instructionsFrom(req),
		Store:        false,
		Stream:       true, // the codex backend requires stream:true upstream
		Temperature:  req.Temperature,
	}
	// NOTE: the codex backend REJECTS max_output_tokens (verified with a live
	// request: "Unsupported parameter: max_output_tokens"). Claude Code requests
	// a very large output budget by default, but the backend applies its own
	// output ceiling — so the field is deliberately not sent.

	// tools: wrap in the Responses shape. The codex backend accepts the standard
	// {type:"function",name,description,parameters} tool defs.
	if len(req.Tools) > 0 {
		var raw []json.RawMessage
		if err := json.Unmarshal(req.Tools, &raw); err == nil && len(raw) > 0 {
			tools := make([]json.RawMessage, 0, len(raw))
			for _, t := range raw {
				tools = append(tools, convertTool(t))
			}
			if len(tools) > 0 {
				body.Tools = tools
			}
		}
	}

	var input []json.RawMessage
	var pendingCallIDs []string // function_calls awaiting a function_call_output
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			// Already folded into Instructions; system messages mid-conversation
			// are rare and the backend only accepts one instructions string.
			continue
		case "user":
			item := map[string]any{
				"role":    "user",
				"content": contentToResponses(m.Content),
			}
			raw, _ := json.Marshal(item)
			input = append(input, raw)
		case "assistant":
			// Emit the assistant text, then any function_calls. The Responses API
			// wants them as separate items but in the same turn.
			text := contentString(m.Content)
			if text != "" {
				item := map[string]any{
					"role":    "assistant",
					"content": []map[string]any{{"type": "output_text", "text": text}},
				}
				raw, _ := json.Marshal(item)
				input = append(input, raw)
			}
			for _, tc := range m.ToolCalls {
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				item := map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": args,
					"role":      "assistant",
				}
				raw, _ := json.Marshal(item)
				input = append(input, raw)
				if tc.ID != "" {
					pendingCallIDs = append(pendingCallIDs, tc.ID)
				}
			}
		case "tool":
			// Chat-completions "tool" message -> function_call_output. If the
			// call_id was never announced (compaction dropped the assistant
			// turn), the backend would reject the orphan; drop it instead.
			if m.ToolCallID == "" || !contains(pendingCallIDs, m.ToolCallID) {
				continue
			}
			item := map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  contentString(m.Content),
			}
			raw, _ := json.Marshal(item)
			input = append(input, raw)
		}
	}
	body.Input = input

	return json.Marshal(body)
}

// instructionsFrom collects the system message into a single instructions
// string, as the Responses API expects.
func instructionsFrom(req *openAIRequest) string {
	var parts []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			if s := contentString(m.Content); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// contentToResponses converts a chat-completions content value (string or
// blocks) into the Responses "content" array shape. The backend accepts both a
// plain string and an array of {type,input_text,text} items; using the array
// keeps the door open for future image support.
func contentToResponses(content any) any {
	s := contentString(content)
	if s == "" {
		return []map[string]any{{"type": "input_text", "text": ""}}
	}
	return []map[string]any{{"type": "input_text", "text": s}}
}

// convertTool maps a chat-completions tool (already the {type:"function",
// function:{name,parameters}} shape) into a Responses API function tool. The
// two shapes differ only in that Responses tools carry name/description/
// parameters at the top level rather than nested under function.
func convertTool(raw json.RawMessage) json.RawMessage {
	var t struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &t); err != nil || t.Function.Name == "" {
		return raw // pass through; the backend will reject unknown shapes visibly
	}
	params := t.Function.Parameters
	if len(params) == 0 {
		params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	out := map[string]any{
		"type":       "function",
		"name":       t.Function.Name,
		"parameters": params,
		"strict":     false,
	}
	if t.Function.Description != "" {
		out["description"] = t.Function.Description
	}
	b, _ := json.Marshal(out)
	return b
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Upstream transport: POST {base}/responses with Bearer + ChatGPT-Account-ID.
// ---------------------------------------------------------------------------

// responseTransport adds the codex backend's extra headers to a request bound
// for the Responses endpoint.
type responseTransport struct {
	accountID string
}

func (rt *responseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", rt.accountID)
	}
	// The backend requires the request be streaming; the proxy always sets
	// stream:true via toResponses. Ensure the Accept header is set for the SSE
	// response so the backend streams rather than buffering.
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	return http.DefaultTransport.RoundTrip(req)
}

// ---------------------------------------------------------------------------
// Response translation: Responses SSE -> Chat Completions SSE chunks.
// ---------------------------------------------------------------------------

// responsesSSEStream converts the Responses API SSE stream into the
// chat-completions SSE chunk stream the rest of the proxy expects. It is the
// inverse of toResponses and lets streamTranslate consume a codex-sub response
// unchanged.
// newScanner returns a line scanner configured for large SSE lines (tool
// arguments can be big).
func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return sc
}

func responsesSSEStream(ctx context.Context, body io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		sc := newScanner(body)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			chunks, ok := responsesEventToChatChunks(payload)
			if !ok {
				continue
			}
			for _, c := range chunks {
				raw, err := json.Marshal(c)
				if err != nil {
					continue
				}
				select {
				case <-ctx.Done():
					return // relay abandoned downstream; stop feeding a dead pipe
				default:
				}
				if _, err := pw.Write([]byte("data: " + string(raw) + "\n\n")); err != nil {
					return // reader gone (e.g. scanner aborted past 8MB line)
				}
			}
		}
		if err := sc.Err(); err != nil {
			// Upstream broke mid-stream: propagate the error through the pipe so
			// streamTranslate aborts with an error event. Writing [DONE] here —
			// as this path once did unconditionally — would mask every mid-stream
			// cut as a clean completion and Claude Code would accept a half answer.
			pw.CloseWithError(err)
			return
		}
		// Terminate the chat-completions stream like any gateway would.
		_, _ = io.WriteString(pw, "data: [DONE]\n\n")
	}()
	return pr
}

// responsesEventToChatChunks maps one Responses SSE event payload to zero or
// more chat-completions stream chunks. It returns ok=false when the event
// carries nothing the chat-completions shape needs (e.g. a response.created
// or error-session event).
func responsesEventToChatChunks(payload string) ([]chatChunk, bool) {
	var ev responsesEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return nil, false
	}
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta == "" {
			return nil, false
		}
		return []chatChunk{{
			ID: "msg_codex",
			Choices: []chatChoice{{
				Index: 0,
				Delta: chatDelta{Content: ev.Delta},
			}},
		}}, true
	case "response.output_item.done":
		if ev.Item.Type != "function_call" {
			return nil, false
		}
		args := ev.Item.Arguments
		if args == "" {
			args = "{}"
		}
		return []chatChunk{{
			ID: "msg_codex",
			Choices: []chatChoice{{
				Index: 0,
				Delta: chatDelta{ToolCalls: []chatTool{{
					Index: 0,
					ID:    ev.Item.CallID,
					Type:  "function",
					Function: chatToolFunction{
						Name:      ev.Item.Name,
						Arguments: args,
					},
				}}},
			}},
		}}, true
	case "response.completed":
		if ev.Response == nil {
			return nil, false
		}
		return []chatChunk{{
			ID: "msg_codex",
			Choices: []chatChoice{{
				Index: 0,
				Delta: chatDelta{},
			}},
			Usage: &chatUsage{
				PromptTokens:     ev.Response.Usage.InputTokens,
				CompletionTokens: ev.Response.Usage.OutputTokens,
			},
		}}, true
	default:
		return nil, false
	}
}

// chatChunk / chatChoice / chatDelta / chatTool / chatUsage mirror the
// chat-completions streaming chunk shape streamState already parses.
type chatChunk struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Index int       `json:"index"`
	Delta chatDelta `json:"delta"`
}

type chatDelta struct {
	Content   string     `json:"content,omitempty"`
	ToolCalls []chatTool `json:"tool_calls,omitempty"`
}

type chatTool struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// responsesEvent is one SSE event from the Responses API. Only the fields the
// chat-completions translation needs are modeled; unknown fields are ignored.
type responsesEvent struct {
	Type string `json:"type"`
	// response.output_text.delta
	Delta string `json:"delta"`
	// response.output_item.done
	Item struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	// response.completed
	Response *struct {
		ID    string `json:"id"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// sendResponses posts a translated Responses request to the upstream and
// returns the raw HTTP response (streamed). Used only by the codex-sub kind.
func sendResponses(ctx context.Context, upstream Upstream, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.BaseURL+responsesPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &responseTransport{accountID: upstream.AccountID}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	// The codex backend always streams; if a non-2xx status comes back, read and
	// return the body so the caller can classify it.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, nil
}
