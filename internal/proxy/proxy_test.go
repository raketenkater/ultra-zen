package proxy

import (
	"encoding/json"
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
