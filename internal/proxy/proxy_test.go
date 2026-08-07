package proxy

import (
	"encoding/json"
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
		"stop":       "end_turn",
		"tool_calls": "tool_use",
		"length":     "max_tokens",
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
		"stop_with_tool_calls":   `{"id":"c1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"tu1","function":{"name":"spawn_agent","arguments":"{}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
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
