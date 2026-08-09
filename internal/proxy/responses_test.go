package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToResponsesBasic(t *testing.T) {
	oreq := &openAIRequest{
		Model:     "gpt-5.6-sol",
		MaxTokens: 1024,
		Messages: []openAIMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}
	body, err := toResponses(oreq)
	if err != nil {
		t.Fatalf("toResponses: %v", err)
	}
	var r responsesRequest
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Model != "gpt-5.6-sol" || r.Instructions != "You are helpful." {
		t.Fatalf("model/instructions = %q / %q", r.Model, r.Instructions)
	}
	if !r.Stream || r.Store {
		t.Fatalf("stream=%v store=%v, want stream:true store:false", r.Stream, r.Store)
	}
	if len(r.Input) != 2 {
		t.Fatalf("input = %d items, want 2 (system folded into instructions)", len(r.Input))
	}
	// first input is the user message
	var first map[string]any
	_ = json.Unmarshal(r.Input[0], &first)
	if first["role"] != "user" {
		t.Fatalf("first input role = %v", first["role"])
	}
}

func TestToResponsesToolsAndToolResults(t *testing.T) {
	// tools: pass as already-built JSON
	tools := json.RawMessage(`[
		{"type":"function","function":{"name":"search","description":"find","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}
	]`)
	oreq := &openAIRequest{
		Model: "gpt-5.6-sol",
		Tools: tools,
		Messages: []openAIMessage{
			{Role: "user", Content: "search now"},
			{Role: "assistant", Content: "", ToolCalls: []openAITool{{
				ID: "call_1", Type: "function",
				Function: openAIToolFunc{Name: "search", Arguments: `{"q":"x"}`},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
		},
	}
	body, err := toResponses(oreq)
	if err != nil {
		t.Fatalf("toResponses: %v", err)
	}
	var r responsesRequest
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(r.Tools))
	}
	var tool map[string]any
	_ = json.Unmarshal(r.Tools[0], &tool)
	if tool["name"] != "search" || tool["type"] != "function" {
		t.Fatalf("tool = %+v", tool)
	}
	if tool["strict"] != false {
		t.Fatalf("tool strict = %v, want false", tool["strict"])
	}
	// 3 input items: user, function_call (empty assistant text emits no text
	// item), function_call_output.
	if len(r.Input) != 3 {
		t.Fatalf("input = %d items, want 3", len(r.Input))
	}
	// The middle item is the function_call.
	var call map[string]any
	_ = json.Unmarshal(r.Input[1], &call)
	if call["type"] != "function_call" || call["name"] != "search" || call["call_id"] != "call_1" {
		t.Fatalf("function_call item = %+v", call)
	}
	var last map[string]any
	_ = json.Unmarshal(r.Input[len(r.Input)-1], &last)
	if last["type"] != "function_call_output" || last["call_id"] != "call_1" {
		t.Fatalf("last input = %+v, want function_call_output call_1", last)
	}
}

func TestResponsesEventToChatChunks(t *testing.T) {
	cases := []struct {
		name string
		ev   string
		want string // substring the chunk should contain
	}{
		{"text", `{"type":"response.output_text.delta","delta":"Hello"}`, `"content":"Hello"`},
		{"tool", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"search","arguments":"{\"q\":\"x\"}"}}`, `"name":"search"`},
		{"completed", `{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":10,"output_tokens":20}}}`, `"prompt_tokens":10`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chunks, ok := responsesEventToChatChunks(c.ev)
			if !ok {
				t.Fatalf("responsesEventToChatChunks returned ok=false")
			}
			if len(chunks) == 0 {
				t.Fatalf("no chunks")
			}
			raw, _ := json.Marshal(chunks[0])
			if !strings.Contains(string(raw), c.want) {
				t.Fatalf("chunk %s missing %q", raw, c.want)
			}
		})
	}
}

func TestResponsesSSEStreamTranslation(t *testing.T) {
	sse := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n"
	out := responsesSSEStream(strings.NewReader(sse))
	data, _ := io.ReadAll(out)
	s := string(data)
	if !strings.Contains(s, `"content":"Hello"`) {
		t.Fatalf("translated stream missing text: %s", s)
	}
	if !strings.Contains(s, `"prompt_tokens":1`) {
		t.Fatalf("translated stream missing usage: %s", s)
	}
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("translated stream missing [DONE] terminator")
	}
}

func TestNonStreamResponsesToAnthropic(t *testing.T) {
	// Simulate the buffered Responses SSE the backend returns for a non-stream
	// caller: text + completed usage.
	sse := `data: {"type":"response.output_text.delta","delta":"Hi"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":5,"output_tokens":3}}}` + "\n\n"
	chunks := collectChatChunks([]byte(sse))
	oresp := chunksToOpenAIResponse(chunks)
	anthropic := oresp.toAnthropic("gpt-5.6-sol")
	if anthropic.Content[0].Text != "Hi" {
		t.Fatalf("text = %q, want Hi", anthropic.Content[0].Text)
	}
	if anthropic.Usage.InputTokens != 5 || anthropic.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", anthropic.Usage)
	}
}

func TestForwardToResponsesKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("ChatGPT-Account-ID") != "acct" {
			t.Errorf("ChatGPT-Account-ID = %q", r.Header.Get("ChatGPT-Account-ID"))
		}
		var body responsesRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			t.Errorf("stream not true upstream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	s := &Server{}
	up := Upstream{BaseURL: srv.URL, APIKey: "tok", AccountID: "acct", Kind: UpstreamResponses}
	oreq := &openAIRequest{Model: "gpt-5.6-sol", Messages: []openAIMessage{{Role: "user", Content: "hi"}}}
	_, resp, err := s.forwardTo(t.Context(), up, oreq)
	if err != nil {
		t.Fatalf("forwardTo: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "response.output_text.delta") {
		t.Fatalf("upstream not hit: %s", data)
	}
}
