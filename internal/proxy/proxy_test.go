package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToOpenAISystem(t *testing.T) {
	req := &anthropicRequest{
		Model:     "claude-sonnet",
		MaxTokens: 100,
		System:    json.RawMessage(`"You are helpful."`),
		Messages:  []anthropicMsg{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Model != "glm-5.1" {
		t.Fatalf("model not overridden: %q", o.Model)
	}
	if len(o.Messages) != 2 {
		t.Fatalf("expected system+user, got %d", len(o.Messages))
	}
	if o.Messages[0].Role != "system" || o.Messages[0].Content != "You are helpful." {
		t.Fatalf("system message wrong: %+v", o.Messages[0])
	}
}

// TestTruncateToContext verifies the over-limit rescue: a request whose context
// exceeds the model's window has its OLDEST non-system messages trimmed (system
// + most recent kept) so the gateway accepts it instead of hard-failing.
func TestTruncateToContext(t *testing.T) {
	mid := strings.Repeat("x", 1000) // ~250 tokens per message
	req := &openAIRequest{
		MaxTokens: 1000,
		Messages: []openAIMessage{
			{Role: "system", Content: "you are a helper"},
			{Role: "user", Content: "old " + mid},
			{Role: "assistant", Content: "reply " + mid},
			{Role: "user", Content: "old2 " + mid},
			{Role: "assistant", Content: "reply2 " + mid},
			{Role: "user", Content: "NEWEST question"},
		},
	}
	// Window ~2500 tokens; 5 non-system messages at ~250 each = ~1250, so the
	// budget (~2500-1000-1024=476) trims the oldest and keeps the newest.
	note := req.truncateToContext(2500, 1000)
	if note == "" {
		t.Fatalf("expected truncation note for over-limit request")
	}
	// System + at least the newest user must survive.
	if len(req.Messages) < 2 {
		t.Fatalf("kept %d messages, want >=2 (system + newest)", len(req.Messages))
	}
	if req.Messages[0].Role != "system" {
		t.Fatalf("system message lost: %+v", req.Messages[0])
	}
	last := req.Messages[len(req.Messages)-1]
	if s, ok := last.Content.(string); !ok || s != "NEWEST question" {
		t.Fatalf("newest message lost: %+v", last)
	}
	// The oldest user message ("old ...") should be gone.
	for _, m := range req.Messages {
		if s, ok := m.Content.(string); ok && strings.HasPrefix(s, "old ") {
			t.Fatalf("old message survived truncation: %q", s[:20])
		}
	}
}

// TestTruncateToContextNoOpWhenFits verifies a small request is left untouched.
func TestTruncateToContextNoOpWhenFits(t *testing.T) {
	req := &openAIRequest{
		MaxTokens: 100,
		Messages: []openAIMessage{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "hello"},
		},
	}
	if note := req.truncateToContext(1_000_000, 100); note != "" {
		t.Fatalf("small request should not truncate, got %q", note)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages changed: %d", len(req.Messages))
	}
}

// TestTruncateToContextReducesOutputBeforeHistory verifies that Claude Code's
// oversized max_tokens request consumes the remaining window before any
// conversation messages are discarded.
func TestTruncateToContextReducesOutputBeforeHistory(t *testing.T) {
	req := &openAIRequest{
		MaxTokens: 8000,
		Messages: []openAIMessage{
			{Role: "system", Content: "keep the whole conversation"},
			{Role: "user", Content: strings.Repeat("context ", 1500)},
			{Role: "assistant", Content: "still relevant"},
			{Role: "user", Content: "new question"},
		},
	}
	wantMessages := len(req.Messages)
	note := req.truncateToContext(10_000, req.MaxTokens)
	if !strings.Contains(note, "reduced max_tokens") {
		t.Fatalf("note = %q, want output-budget reduction", note)
	}
	if len(req.Messages) != wantMessages {
		t.Fatalf("history was discarded: got %d messages, want %d", len(req.Messages), wantMessages)
	}
	if req.MaxTokens >= 8000 || req.MaxTokens < 1024 {
		t.Fatalf("max_tokens = %d, want a useful reduced allowance", req.MaxTokens)
	}
}

// TestTruncateToContextKeepsToolTurnsAtomic verifies emergency trimming never
// leaves a tool result without the assistant tool_call that announced it.
func TestTruncateToContextKeepsToolTurnsAtomic(t *testing.T) {
	req := &openAIRequest{
		MaxTokens: 4000,
		Messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "old task " + strings.Repeat("x", 7000)},
			{Role: "assistant", ToolCalls: []openAITool{{
				ID: "call_old", Type: "function",
				Function: openAIToolFunc{Name: "read_file", Arguments: `{"path":"old"}`},
			}}},
			{Role: "tool", ToolCallID: "call_old", Content: strings.Repeat("result", 1400)},
			{Role: "assistant", Content: "old answer"},
			{Role: "user", Content: "newest question"},
		},
	}
	note := req.truncateToContext(6000, req.MaxTokens)
	if !strings.Contains(note, "complete old turn") {
		t.Fatalf("note = %q, want complete-turn rescue", note)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %+v, want system + newest user only", req.Messages)
	}
	if req.Messages[0].Role != "system" || !strings.Contains(req.Messages[0].Content.(string), "context rescue") {
		t.Fatalf("missing explicit rescue note: %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "newest question" {
		t.Fatalf("newest turn not preserved: %+v", req.Messages[1])
	}
}

func TestToOpenAIToolUseAndResult(t *testing.T) {
	req := &anthropicRequest{
		Model:     "claude-sonnet",
		MaxTokens: 200,
		Messages: []anthropicMsg{
			{Role: "user", Content: json.RawMessage(`"weather?"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"tu1","name":"get_weather","input":{"city":"Paris"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"tu1","content":"Sunny"}]`)},
		},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	// user + assistant(tool_calls) + tool  (no system message in this request)
	if len(o.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(o.Messages), o.Messages)
	}
	asst := o.Messages[1]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls wrong: %+v", asst)
	}
	if asst.ToolCalls[0].Function.Name != "get_weather" || asst.ToolCalls[0].ID != "tu1" {
		t.Fatalf("tool call fields wrong: %+v", asst.ToolCalls[0])
	}
	toolMsg := o.Messages[2]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "tu1" || toolMsg.Content != "Sunny" {
		t.Fatalf("tool result message wrong: %+v", toolMsg)
	}
}

func TestToOpenAIToolsAndChoice(t *testing.T) {
	req := &anthropicRequest{
		Model: "claude-sonnet",
		Tools: []anthropicTool{
			{Name: "f", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"f"}`),
		Messages:   []anthropicMsg{{Role: "user", Content: json.RawMessage(`"x"`)}},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Tools) == 0 {
		t.Fatal("tools not translated")
	}
	if !strings.Contains(string(o.Tools), `"type":"function"`) {
		t.Fatalf("tool not wrapped: %s", o.Tools)
	}
	if !strings.Contains(string(o.ToolChoice), `"function"`) {
		t.Fatalf("tool_choice not mapped: %s", o.ToolChoice)
	}
}

func TestToAnthropicToolUse(t *testing.T) {
	raw := `{"id":"c1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"tu1","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	var o openAIResponse
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	a := o.toAnthropic("claude-sonnet")
	if a.StopReason != "tool_use" {
		t.Fatalf("stop_reason=%q", a.StopReason)
	}
	if len(a.Content) != 1 || a.Content[0].Type != "tool_use" {
		t.Fatalf("content wrong: %+v", a.Content)
	}
	if a.Content[0].Name != "get_weather" || a.Content[0].ID != "tu1" {
		t.Fatalf("tool_use fields wrong: %+v", a.Content[0])
	}
	if string(a.Content[0].Input) != `{"city":"Paris"}` {
		t.Fatalf("tool input wrong: %s", a.Content[0].Input)
	}
	if a.Usage.InputTokens != 10 || a.Usage.OutputTokens != 5 {
		t.Fatalf("usage wrong: %+v", a.Usage)
	}
}

func TestToAnthropicReasoningFallback(t *testing.T) {
	raw := `{"id":"c1","choices":[{"message":{"role":"assistant","content":"","reasoning_content":"thinking here","tool_calls":[]},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
	var o openAIResponse
	json.Unmarshal([]byte(raw), &o)
	a := o.toAnthropic("m")
	if len(a.Content) != 1 || a.Content[0].Text != "thinking here" {
		t.Fatalf("reasoning not surfaced: %+v", a.Content)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"stop":           "end_turn",
		"tool_calls":     "tool_use",
		"length":         "max_tokens",
		"content_filter": "end_turn",
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Fatalf("mapStopReason(%q)=%q want %q", in, got, want)
		}
	}
}

// TestStreamStopReasonToolUseWhenToolEmitted verifies the stream path stays
// tool-aware: an SSE stream that emits tool_calls deltas but never a final
// finish_reason chunk (the gateway ends with a usage-only chunk or [DONE]) must
// produce an Anthropic message_delta with stop_reason="tool_use". Otherwise
// Claude Code treats the turn as finished and never runs the subagent/ MCP call.
func TestStreamStopReasonToolUseWhenToolEmitted(t *testing.T) {
	sse := "data: " + `{"id":"c1","choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
		"data: " + `{"id":"c1","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tu1","type":"function","function":{"name":"spawn_agent","arguments":"{}"}}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"

	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	if err := streamTranslate(rec, strings.NewReader(sse), "claude-sonnet"); err != nil {
		t.Fatal(err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("message_delta missing stop_reason=tool_use; got: %s", out)
	}
	if !strings.Contains(out, `"type":"tool_use"`) || !strings.Contains(out, `"name":"spawn_agent"`) {
		t.Fatalf("tool_use content block missing; got: %s", out)
	}
}

// TestToAnthropicToolUseWithMissingFinishReason covers the case that breaks
// subagent spawn / MCP research: a gateway emits tool_calls but finishes with
// stop (or omits finish_reason). The stop_reason must still be "tool_use" or
// Claude Code never executes the pending tool call.
func TestToAnthropicToolUseWithMissingFinishReason(t *testing.T) {
	for name, raw := range map[string]string{
		"stop_with_tool_calls":    `{"id":"c1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"tu1","function":{"name":"spawn_agent","arguments":"{}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		"empty_finish_tool_calls": `{"id":"c1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"tu1","function":{"name":"spawn_agent","arguments":"{}"}}]},"finish_reason":""}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var o openAIResponse
			if err := json.Unmarshal([]byte(raw), &o); err != nil {
				t.Fatal(err)
			}
			a := o.toAnthropic("claude-sonnet")
			if a.StopReason != "tool_use" {
				t.Fatalf("stop_reason=%q, want tool_use (pending tool call must run)", a.StopReason)
			}
			if len(a.Content) != 1 || a.Content[0].Type != "tool_use" {
				t.Fatalf("content wrong: %+v", a.Content)
			}
		})
	}
}

// TestToolResultMissingIDGetsRepaired covers the gateway 400
// "messages[N]: missing field tool_call_id": a tool_result block whose
// tool_use_id is absent would serialize as a "tool" message with no
// tool_call_id (the field is omitempty). It must adopt the pending
// tool_call id from the preceding assistant turn.
func TestToolResultMissingIDGetsRepaired(t *testing.T) {
	req := &anthropicRequest{
		Messages: []anthropicMsg{
			{Role: "user", Content: json.RawMessage(`"run it"`)},
			{Role: "assistant", Content: json.RawMessage(
				`[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]`)},
			{Role: "user", Content: json.RawMessage(
				`[{"type":"tool_result","content":"files"}]`)},
		},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range o.Messages {
		if m.Role == "tool" && m.ToolCallID == "" {
			t.Fatalf("messages[%d] is a tool message with no tool_call_id: %+v", i, m)
		}
	}
	last := o.Messages[len(o.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "toolu_1" {
		t.Fatalf("result did not adopt the pending id: %+v", last)
	}
	// The serialized body must actually carry the field.
	body, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"tool_call_id":"toolu_1"`) {
		t.Fatalf("tool_call_id missing from body: %s", body)
	}
}

// TestOrphanToolResultDemoted verifies a tool_result with no preceding
// assistant tool_call (history compaction dropped it) becomes a user message
// instead of an invalid tool turn.
func TestOrphanToolResultDemoted(t *testing.T) {
	req := &anthropicRequest{
		Messages: []anthropicMsg{
			{Role: "user", Content: json.RawMessage(
				`[{"type":"tool_result","tool_use_id":"toolu_gone","content":"stale output"}]`)},
		},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(o.Messages), o.Messages)
	}
	if o.Messages[0].Role != "user" || o.Messages[0].Content != "stale output" {
		t.Fatalf("orphan not demoted: %+v", o.Messages[0])
	}
}

// TestUnknownToolResultIDAdoptsPending verifies a tool_result whose id does not
// match the assistant's announced call still answers that call.
func TestUnknownToolResultIDAdoptsPending(t *testing.T) {
	req := &anthropicRequest{
		Messages: []anthropicMsg{
			{Role: "assistant", Content: json.RawMessage(
				`[{"type":"tool_use","id":"toolu_a","name":"Read","input":{}}]`)},
			{Role: "user", Content: json.RawMessage(
				`[{"type":"tool_result","tool_use_id":"toolu_mismatch","content":"ok"}]`)},
		},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	last := o.Messages[len(o.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "toolu_a" {
		t.Fatalf("mismatched id not repaired: %+v", last)
	}
	if len(o.Messages) != 2 {
		t.Fatalf("unexpected stub inserted: %+v", o.Messages)
	}
}

// TestMultipleToolResultsKeepDistinctIDs verifies parallel tool calls answered
// by results that all lack ids get one pending id each, not the same one.
func TestMultipleToolResultsKeepDistinctIDs(t *testing.T) {
	req := &anthropicRequest{
		Messages: []anthropicMsg{
			{Role: "assistant", Content: json.RawMessage(
				`[{"type":"tool_use","id":"t1","name":"Bash","input":{}},` +
					`{"type":"tool_use","id":"t2","name":"Bash","input":{}}]`)},
			{Role: "user", Content: json.RawMessage(
				`[{"type":"tool_result","content":"a"},{"type":"tool_result","content":"b"}]`)},
		},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Messages) != 3 {
		t.Fatalf("expected assistant+2 tool messages, got %d: %+v", len(o.Messages), o.Messages)
	}
	if o.Messages[1].ToolCallID != "t1" || o.Messages[2].ToolCallID != "t2" {
		t.Fatalf("ids not distributed in order: %+v %+v", o.Messages[1], o.Messages[2])
	}
}

// TestTruncatedToolArgumentsStaySerializable covers a model that hit
// max_tokens mid-arguments. Splicing the truncated fragment into the
// Anthropic response made json.Marshal fail, and the client got a 200 with an
// empty body instead of an error.
func TestTruncatedToolArgumentsStaySerializable(t *testing.T) {
	for _, args := range []string{`{"cmd": "l`, ``, `null`, `"scalar"`, `[1,2]`} {
		r := &openAIResponse{}
		r.Choices = append(r.Choices, struct {
			Index   int `json:"index"`
			Message struct {
				Role      string       `json:"role"`
				Content   string       `json:"content"`
				ToolCalls []openAITool `json:"tool_calls"`
				Reasoning string       `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{})
		r.Choices[0].Message.ToolCalls = []openAITool{
			{ID: "t1", Type: "function", Function: openAIToolFunc{Name: "Bash", Arguments: args}},
		}
		out, err := json.Marshal(r.toAnthropic("m"))
		if err != nil {
			t.Fatalf("arguments %q made the response unserializable: %v", args, err)
		}
		if !strings.Contains(string(out), `"input":{`) {
			t.Fatalf("arguments %q produced a non-object input: %s", args, out)
		}
	}
}

// TestValidToolArgumentsPreserved verifies the sanitizer does not clobber
// well-formed arguments.
func TestValidToolArgumentsPreserved(t *testing.T) {
	if got := string(toolInput(`{"command":"ls -la"}`)); got != `{"command":"ls -la"}` {
		t.Fatalf("valid arguments altered: %s", got)
	}
}

// TestToolUseWithoutInputSendsEmptyObject verifies a tool_use block with no
// input field becomes "{}" arguments, not "null" (which providers reject).
func TestToolUseWithoutInputSendsEmptyObject(t *testing.T) {
	req := &anthropicRequest{
		Messages: []anthropicMsg{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"t1","name":"Bash"}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]`)},
		},
	}
	o, err := req.toOpenAI("glm-5.1")
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Messages[0].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("arguments = %q, want {}", got)
	}
}

// TestModelSwitchSelectsUpstream verifies the modelRoute map resolves an
// advertised model id (via /model) to the right upstream, including the
// provider-qualified spelling.
func TestModelSwitchSelectsUpstream(t *testing.T) {
	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  "https://zen.example/v1",
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: "https://zen.example/v1", APIKey: "k", Model: "glm-5.2"},
			{Provider: "openrouter", BaseURL: "https://openrouter.example/v1", APIKey: "or", Model: "poolside/laguna-s-2.1:free"},
		},
	}
	s := New(cfg)

	for _, tc := range []struct {
		id       string
		wantBase string
		wantKey  string
	}{
		{"glm-5.2", "https://zen.example/v1", "k"},
		{"opencode-go/glm-5.2", "https://zen.example/v1", "k"},
		{"openrouter/poolside/laguna-s-2.1:free", "https://openrouter.example/v1", "or"},
		{"deepseek-v4-flash", "https://zen.example/v1", "k"},
		// The claude-prefixed ids advertised at /v1/models must route back to
		// the same upstreams (Claude Code's /model sends these back).
		{"claude-opencode-go-glm-5.2", "https://zen.example/v1", "k"},
		{"claude-openrouter-poolside-laguna-s-2.1-free", "https://openrouter.example/v1", "or"},
		{"claude-opencode-go-deepseek-v4-flash", "https://zen.example/v1", "k"},
	} {
		u, ok := s.modelRoute[tc.id]
		if !ok {
			t.Fatalf("%q not in modelRoute", tc.id)
		}
		if u.BaseURL != tc.wantBase || u.APIKey != tc.wantKey {
			t.Fatalf("%q -> %+v, want base=%q key=%q", tc.id, u, tc.wantBase, tc.wantKey)
		}
	}
}

// TestHandleModelsAdvertisesClaudePrefixedIDs verifies /v1/models advertises
// ids that survive Claude Code's /(claude|anthropic)/i discovery filter and
// includes a group header per provider.
func TestHandleModelsAdvertisesClaudePrefixedIDs(t *testing.T) {
	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  "https://zen.example/v1",
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Models: []ModelInfo{
			{ID: "deepseek-v4-flash", Name: "deepseek-v4-flash", Provider: "opencode-go", ContextLength: 1_000_000},
			{ID: "glm-5.2", Name: "glm-5.2", Provider: "opencode-go", ContextLength: 200_000},
			{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol", Provider: "codex", ContextLength: 272_000},
		},
	}
	s := New(cfg)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	s.handleModels(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"display_name"`
			Disabled      bool   `json:"disabled"`
			ContextWindow int    `json:"context_window"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Every model id must contain "claude" (survive the /model filter), and the
	// codex + opencode group headers must appear as disabled (non-selectable).
	ids := map[string]bool{}
	var hasHeader bool
	for _, m := range payload.Data {
		ids[m.ID] = true
		if m.ID == "claude-group-opencode-go" || m.ID == "claude-group-codex" {
			hasHeader = true
			if !m.Disabled {
				t.Fatalf("group header %q must be disabled (non-selectable)", m.ID)
			}
		}
	}
	if !hasHeader {
		t.Fatalf("missing group headers; ids=%v", ids)
	}
	// Real model rows must NOT be disabled.
	for _, m := range payload.Data {
		if m.Disabled && (m.ID == "claude-opencode-go-deepseek-v4-flash" || m.ID == "claude-codex-gpt-5.6-sol") {
			t.Fatalf("model row %q must not be disabled", m.ID)
		}
	}
	for _, want := range []string{
		"claude-opencode-go-deepseek-v4-flash",
		"claude-opencode-go-glm-5.2",
		"claude-codex-gpt-5.6-sol",
	} {
		if !ids[want] {
			t.Fatalf("missing advertised id %q; ids=%v", want, ids)
		}
	}
	// Display names identify both the model and its provider.
	nameByID := map[string]string{}
	ctxByID := map[string]int{}
	for _, m := range payload.Data {
		nameByID[m.ID] = m.Name
		ctxByID[m.ID] = m.ContextWindow
	}
	if got := nameByID["claude-codex-gpt-5.6-sol"]; got != "GPT-5.6-Sol — Codex (ChatGPT sub)" {
		t.Fatalf("codex display name = %q, want provider-labelled", got)
	}
	// The real context window must be advertised so Claude Code autocompacts at
	// the right point (deepseek-v4-flash is 1M, glm-5.2 is 200k).
	if got := ctxByID["claude-opencode-go-deepseek-v4-flash"]; got != 1_000_000 {
		t.Fatalf("deepseek-v4-flash context_window = %d, want 1000000 (autocompaction)", got)
	}
	if got := ctxByID["claude-opencode-go-glm-5.2"]; got != 200_000 {
		t.Fatalf("glm-5.2 context_window = %d, want 200000", got)
	}
	// Group headers carry no context.
	if got := ctxByID["claude-group-opencode-go"]; got != 0 {
		t.Fatalf("group header context_window = %d, want 0", got)
	}
}

// TestMessagesGroupHeaderFails verifies a request whose model is a disabled
// group header (claude-group-*) is rejected loudly instead of silently routing
// to the primary.
func TestMessagesGroupHeaderFails(t *testing.T) {
	srv := New(Config{
		Provider: "opencode-go",
		BaseURL:  "https://zen.example/v1",
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"claude-group-openrouter","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMessages(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (group header is not selectable)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "section header") {
		t.Fatalf("error should mention section header: %s", rec.Body.String())
	}
}

// TestMessagesUnknownModelKeepsPrimary verifies an arbitrary unrecognized model
// id (not a group header) still falls back to the primary, matching the
// pre-existing behavior for other clients.
func TestMessagesUnknownModelKeepsPrimary(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer backend.Close()

	srv := New(Config{
		Provider: "opencode-go",
		BaseURL:  backend.URL,
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"some-other-client-model","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (unknown id falls back to primary)", rec.Code)
	}
}

// TestRouteOrderPutsSelectedFirst verifies that when /model selects a fallback,
// routeOrder tries that upstream first without duplicating it in the pool.
func TestRouteOrderPutsSelectedFirst(t *testing.T) {
	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  "https://zen.example/v1",
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: "https://zen.example/v1", APIKey: "k", Model: "glm-5.2"},
			{Provider: "openrouter", BaseURL: "https://openrouter.example/v1", APIKey: "or", Model: "poolside/laguna-s-2.1:free"},
		},
	}
	s := New(cfg)

	// Select glm-5.2 (a fallback) as the request's primary. The pool has 3
	// entries (primary + 2 fallbacks), so 3 routes — but glm-5.2 first and
	// only once.
	sel := s.modelRoute["glm-5.2"]
	routes := s.routeOrder(sel)
	if len(routes) != 3 {
		t.Fatalf("expected 3 distinct routes, got %d: %+v", len(routes), routes)
	}
	if routes[0].Upstream.Model != "glm-5.2" {
		t.Fatalf("first route should be the selected model, got %+v", routes[0])
	}
	// glm-5.2 must appear exactly once (not duplicated from the pool).
	var count int
	for _, r := range routes {
		if r.Upstream.Model == "glm-5.2" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("glm-5.2 appears %d times, want 1", count)
	}
}

// TestHandleMessagesHonorsRequestModel runs the full /v1/messages handler
// against a stub gateway and verifies the forwarded request uses the /model
// selected upstream.
func TestHandleMessagesHonorsRequestModel(t *testing.T) {
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var oreq openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&oreq); err != nil {
			t.Errorf("zen decode: %v", err)
		}
		if oreq.Model != "glm-5.2" {
			t.Errorf("zen got model %q, want glm-5.2", oreq.Model)
		}
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer zen.Close()
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"or"}}]}`))
	}))
	defer or.Close()

	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  zen.URL,
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: zen.URL, APIKey: "k", Model: "glm-5.2"},
			{Provider: "openrouter", BaseURL: or.URL, APIKey: "or", Model: "poolside/laguna-s-2.1:free"},
		},
	}
	s := New(cfg)
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("expected zen's response, got %s", rec.Body.String())
	}
}

// TestModelSelectedRouteFallsBackOn429 verifies that when the /model-selected
// upstream hits a temporary 429, the pool still rotates to a healthy fallback
// instead of returning the 429 to Claude Code.
func TestModelSelectedRouteFallsBackOn429(t *testing.T) {
	var selCalls, fbCalls int
	sel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selCalls++
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
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
		t.Fatalf("status = %d, want 200 after fallback, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fallback-ok") {
		t.Fatalf("expected fallback response, got %s", rec.Body.String())
	}
	if selCalls == 0 || fbCalls == 0 {
		t.Fatalf("expected selected (sel=%d) to fail and fallback (fb=%d) to serve", selCalls, fbCalls)
	}
}

// TestWorkerSplitRoutesBackgroundRequests verifies the orchestrator/worker
// split: a request with no interactive tools (a background sub-agent) that
// carries the primary model id must be sent to the worker model, while a
// request WITH interactive tools (the main loop) stays on the primary. A
// /model-selected id still wins over the worker.
func TestWorkerSplitRoutesBackgroundRequests(t *testing.T) {
	// Collect the models the upstream gateway is called with. The proxy rewrites
	// the echoed model field in the reply back to the client's requested id, so
	// asserting on the response would always show deepseek-v4-flash; what matters
	// is which model the upstream actually receives.
	var gotModels []string
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var oreq openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&oreq); err != nil {
			t.Errorf("zen decode: %v", err)
		}
		gotModels = append(gotModels, oreq.Model)
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer zen.Close()

	cfg := Config{
		Provider:    "opencode-go",
		BaseURL:     zen.URL,
		APIKey:      "k",
		Model:       "deepseek-v4-flash",
		WorkerModel: "mimo-v2.5",
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	send := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleMessages(rec, req)
		return rec.Code
	}

	// Main loop: carries the interactive/UI tools that only appear in the main
	// Claude Code loop (AskUserQuestion, Skill, ...), so it stays on primary.
	if code := send(`{"model":"deepseek-v4-flash","max_tokens":100,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{}}},
			{"name":"AskUserQuestion","description":"ask","input_schema":{"type":"object","properties":{}}}
		]}`); code != 200 {
		t.Fatalf("interactive request status = %d", code)
	}
	// Background sub-agent: same tool list minus the interactive tools -> worker.
	if code := send(`{"model":"deepseek-v4-flash","max_tokens":100,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{}}}]}`); code != 200 {
		t.Fatalf("background request status = %d", code)
	}
	// An explicit model id that is NOT a registered route (glm-5.2, no fallback)
	// must not be hijacked by the worker — it keeps the primary.
	if code := send(`{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("/model request status = %d", code)
	}

	if len(gotModels) != 3 {
		t.Fatalf("expected 3 upstream calls, got %d: %v", len(gotModels), gotModels)
	}
	if gotModels[0] != "deepseek-v4-flash" {
		t.Errorf("main-loop request sent to %q, want deepseek-v4-flash", gotModels[0])
	}
	if gotModels[1] != "mimo-v2.5" {
		t.Errorf("background request sent to %q, want worker mimo-v2.5", gotModels[1])
	}
	if gotModels[2] != "deepseek-v4-flash" {
		t.Errorf("unknown /model id sent to %q, want primary deepseek-v4-flash (not worker)", gotModels[2])
	}
}

// TestHandleModelsAdvertisesAndRoutesFullCatalog verifies that with the full
// catalog fix (buildAdvertisedCatalog feeding cfg.Models + cfg.Upstreams) the
// /v1/models endpoint advertises models from EVERY reachable provider —
// including ones outside the rotation pool — and that buildModelRoute maps each
// advertised claude-prefixed id back to its own gateway.
func TestHandleModelsAdvertisesAndRoutesFullCatalog(t *testing.T) {
	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  "https://zen.example/v1",
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: "https://zen.example/v1", APIKey: "k", Model: "glm-5.2"},
		},
		Models: []ModelInfo{
			{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Provider: "opencode-go", ContextLength: 1_000_000},
			// Non-primary, non-pool providers the catalog now advertises + routes.
			{ID: "glm-4.7", Name: "GLM 4.7", Provider: "saia", ContextLength: 200_000},
			{ID: "llama-3.3-70b", Name: "Llama 3.3 70B", Provider: "groq", ContextLength: 128_000},
		},
		Upstreams: []Upstream{
			{Provider: "opencode-go", BaseURL: "https://zen.example/v1", APIKey: "k", Model: "deepseek-v4-flash"},
			{Provider: "opencode-go", BaseURL: "https://zen.example/v1", APIKey: "k", Model: "glm-5.2"},
			{Provider: "saia", BaseURL: "https://saia.example/v1", APIKey: "saia-key", Model: "glm-4.7"},
			{Provider: "groq", BaseURL: "https://groq.example/v1", APIKey: "groq-key", Model: "llama-3.3-70b", ContextLength: 128_000},
		},
	}
	s := New(cfg)

	// /v1/models must advertise the non-pool saia + groq ids too.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	s.handleModels(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal /v1/models: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range payload.Data {
		ids[m.ID] = true
	}
	// Group headers plus every model, including saia + groq (not just the pool).
	for _, want := range []string{
		"claude-group-opencode-go", "claude-group-saia", "claude-group-groq",
		"claude-opencode-go-deepseek-v4-flash",
		"claude-saia-glm-4.7",
		"claude-groq-llama-3.3-70b",
	} {
		if !ids[want] {
			t.Fatalf("advertised id %q missing from /v1/models; ids=%v", want, ids)
		}
	}
	// Every advertised id must be routable: the non-pool saia + groq ids resolve.
	if u, ok := s.modelRoute[ClaudeModelID("saia", "glm-4.7")]; !ok {
		t.Fatal("claude-saia-glm-4.7 not routable via modelRoute")
	} else if u.APIKey != "saia-key" || u.BaseURL != "https://saia.example/v1" {
		t.Fatalf("saia route = %+v, want saia-key/saia.example", u)
	}
	if u, ok := s.modelRoute[ClaudeModelID("groq", "llama-3.3-70b")]; !ok {
		t.Fatal("claude-groq-llama-3.3-70b not routable via modelRoute")
	} else if u.APIKey != "groq-key" || u.BaseURL != "https://groq.example/v1" {
		t.Fatalf("groq route = %+v, want groq-key/groq.example", u)
	}
}

// TestPrimaryRouteStructurallyPreserved verifies the launch route is unchanged:
// the primary's model id resolves to cfg.primaryUpstream() (value-equality) so
// handleMessages' "u == primary" and the worker split still work with the
// expanded selectable set present.
func TestPrimaryRouteStructurallyPreserved(t *testing.T) {
	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  "https://zen.example/v1",
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Models: []ModelInfo{
			{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Provider: "opencode-go"},
			{ID: "glm-4.7", Name: "GLM 4.7", Provider: "saia"},
		},
		Upstreams: []Upstream{
			{Provider: "opencode-go", BaseURL: "https://zen.example/v1", APIKey: "k", Model: "deepseek-v4-flash"},
			{Provider: "saia", BaseURL: "https://saia.example/v1", APIKey: "saia-key", Model: "glm-4.7"},
		},
	}
	s := New(cfg)
	prim := cfg.primaryUpstream()
	for _, id := range []string{
		cfg.Model,
		cfg.Provider + "/" + cfg.Model,
		ClaudeModelID(cfg.Provider, cfg.Model),
	} {
		if u, ok := s.modelRoute[id]; !ok {
			t.Fatalf("primary id %q not in modelRoute", id)
		} else if u != prim {
			t.Fatalf("primary id %q -> %+v, want exact primary route %+v (worker split)", id, u, prim)
		}
	}
}

// TestSelectedNonPoolRouteIsAttempted verifies the routeOrder index -1 path: a
// /model-selected model that is in cfg.Upstreams but NOT in the rotation pool is
// attempted first (not silently rotated away), carrying the selected model id to
// the right gateway.
func TestSelectedNonPoolRouteIsAttempted(t *testing.T) {
	var saiaGot []string
	saia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var oreq openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&oreq); err != nil {
			t.Errorf("saia decode: %v", err)
		}
		saiaGot = append(saiaGot, oreq.Model)
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"saia-ok"}}]}`))
	}))
	defer saia.Close()
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"zen-ok"}}]}`))
	}))
	defer zen.Close()

	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  zen.URL,
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: zen.URL, APIKey: "k", Model: "glm-5.2"},
		},
		Upstreams: []Upstream{
			{Provider: "opencode-go", BaseURL: zen.URL, APIKey: "k", Model: "deepseek-v4-flash"},
			{Provider: "opencode-go", BaseURL: zen.URL, APIKey: "k", Model: "glm-5.2"},
			{Provider: "saia", BaseURL: saia.URL, APIKey: "saia-key", Model: "glm-4.7"},
		},
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"claude-saia-glm-4.7","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "saia-ok") {
		t.Fatalf("expected the saia route to serve, got %s", rec.Body.String())
	}
	if len(saiaGot) == 0 || saiaGot[0] != "glm-4.7" {
		t.Fatalf("saia upstream got %v, want first call model glm-4.7", saiaGot)
	}
}

// TestSelectedNonPoolRouteRotatesOn429 verifies that when a selected non-pool
// (index -1) route returns a temporary 429, the pool still rotates and a healthy
// fallback serves the request — it must not wedge on the dead selected route.
func TestSelectedNonPoolRouteRotatesOn429(t *testing.T) {
	var saiaCalls, fbCalls int
	saia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saiaCalls++
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer saia.Close()
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fbCalls++
		w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"fallback-ok"}}]}`))
	}))
	defer fb.Close()
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"ratelimited","type":"rate_limit_error"}}`))
	}))
	defer zen.Close()

	cfg := Config{
		Provider: "opencode-go",
		BaseURL:  zen.URL, // primary also 429s so only the fallback can serve
		APIKey:   "k",
		Model:    "deepseek-v4-flash",
		Fallbacks: []Upstream{
			{Provider: "openrouter", BaseURL: fb.URL, APIKey: "or", Model: "vendor/a:free"},
		},
		Upstreams: []Upstream{
			{Provider: "opencode-go", BaseURL: zen.URL, APIKey: "k", Model: "deepseek-v4-flash"},
			{Provider: "openrouter", BaseURL: fb.URL, APIKey: "or", Model: "vendor/a:free"},
			{Provider: "saia", BaseURL: saia.URL, APIKey: "saia-key", Model: "glm-4.7"},
		},
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"claude-saia-glm-4.7","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 after fallback, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fallback-ok") {
		t.Fatalf("expected fallback response, got %s", rec.Body.String())
	}
	if saiaCalls == 0 || fbCalls == 0 {
		t.Fatalf("expected selected saia route (calls=%d) to fail and fallback (calls=%d) to serve", saiaCalls, fbCalls)
	}
}
