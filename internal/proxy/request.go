// Package proxy is the Anthropic-to-OpenAI translation bridge. Claude Code
// speaks the Anthropic Messages API; the opencode Zen gateway speaks OpenAI
// Chat Completions. This package runs an HTTP server (started as a goroutine
// by the launcher) that accepts /v1/messages, translates the request, forwards
// it to the Zen gateway, and translates the response back — including SSE
// streaming and tool-calling.
package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicRequest is the subset of the Anthropic Messages request we translate.
// Unknown fields are preserved via RawMessage passthrough where it matters;
// fields we do not understand are dropped (Claude Code sends a stable set).
type anthropicRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        json.RawMessage `json:"system"` // string OR array of {type:text,text}
	Messages      []anthropicMsg  `json:"messages"`
	Tools         []anthropicTool `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	Stream        bool            `json:"stream"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	TopK          *int            `json:"top_k"`
	StopSequences []string        `json:"stop_sequences"`
	Metadata      json.RawMessage `json:"metadata"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string OR array of blocks
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON schema object
}

// openAIMessage is one message in the OpenAI Chat Completions format.
type openAIMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []openAITool   `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type openAITool struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function openAIToolFunc   `json:"function"`
}

type openAIToolFunc struct {
	Name       string          `json:"name"`
	Arguments  string          `json:"arguments"` // JSON string
}

// openAIRequest is the body sent to the Zen gateway.
type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
}

// toOpenAI translates an Anthropic Messages request into an OpenAI Chat
// Completions request. The model is replaced with the selected Zen model.
func (a *anthropicRequest) toOpenAI(model string) (*openAIRequest, error) {
	req := &openAIRequest{
		Model:       model,
		MaxTokens:   a.MaxTokens,
		Stream:      a.Stream,
		Temperature: a.Temperature,
		TopP:        a.TopP,
		Stop:        a.StopSequences,
	}

	// System: string or array of text blocks -> a single system message.
	if sys := extractText(a.System); sys != "" {
		req.Messages = append(req.Messages, openAIMessage{Role: "system", Content: sys})
	}

	for _, m := range a.Messages {
		translated, err := translateMessage(m)
		if err != nil {
			return nil, err
		}
		req.Messages = append(req.Messages, translated...)
	}

	// Tools: wrap each Anthropic tool in OpenAI's {type:function,function:{...}}.
	if len(a.Tools) > 0 {
		tools := make([]map[string]any, 0, len(a.Tools))
		for _, t := range a.Tools {
			fn := map[string]any{
				"name":       t.Name,
				"parameters": json.RawMessage(t.InputSchema),
			}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			tools = append(tools, map[string]any{
				"type":     "function",
				"function": fn,
			})
		}
		raw, err := json.Marshal(tools)
		if err != nil {
			return nil, err
		}
		req.Tools = raw
	}

	// tool_choice mapping.
	if len(a.ToolChoice) > 0 && string(a.ToolChoice) != "null" {
		req.ToolChoice = translateToolChoice(a.ToolChoice)
	}

	return req, nil
}

// translateMessage turns one Anthropic message into one or more OpenAI messages.
// A user message containing tool_result blocks emits separate "tool" messages
// (one per result) followed by an optional "user" message for any text.
func translateMessage(m anthropicMsg) ([]openAIMessage, error) {
	content := strings.TrimSpace(string(m.Content))
	// String content.
	if len(m.Content) == 0 || content == `""` || content == "null" {
		return []openAIMessage{{Role: m.Role, Content: ""}}, nil
	}
	// If content does not start with '[', it is a plain string.
	if m.Content[0] != '[' {
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			// Not a string; treat as opaque content.
			return []openAIMessage{{Role: m.Role, Content: json.RawMessage(m.Content)}}, nil
		}
		return []openAIMessage{{Role: m.Role, Content: s}}, nil
	}

	// Array of blocks.
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("parse message content array: %w", err)
	}

	var out []openAIMessage
	var assistantToolCalls []openAITool
	var assistantText strings.Builder
	var userText strings.Builder

	for _, b := range blocks {
		typ := jsonString(b["type"])
		switch typ {
		case "text":
			txt := jsonString(b["text"])
			if m.Role == "assistant" {
				assistantText.WriteString(txt)
			} else {
				userText.WriteString(txt)
			}
		case "tool_use":
			id := jsonString(b["id"])
			name := jsonString(b["name"])
			input, _ := json.Marshal(json.RawMessage(b["input"]))
			assistantToolCalls = append(assistantToolCalls, openAITool{
				ID:   id,
				Type: "function",
				Function: openAIToolFunc{
					Name:      name,
					Arguments: string(input),
				},
			})
		case "tool_result":
			id := jsonString(b["tool_use_id"])
			// content may be string or array of text blocks; flatten to a string.
			resContent := flattenResultContent(b["content"])
			out = append(out, openAIMessage{
				Role:       "tool",
				ToolCallID: id,
				Content:    resContent,
			})
		case "image":
			// Zen text models do not accept images; represent as a placeholder
			// so the conversation stays structurally valid.
			userText.WriteString("[image omitted]")
		}
	}

	switch m.Role {
	case "assistant":
		msg := openAIMessage{Role: "assistant"}
		if assistantText.Len() > 0 {
			msg.Content = assistantText.String()
		}
		if len(assistantToolCalls) > 0 {
			msg.ToolCalls = assistantToolCalls
		}
		out = append(out, msg)
	case "user":
		// tool messages were already appended; add trailing user text if any.
		if userText.Len() > 0 {
			out = append(out, openAIMessage{Role: "user", Content: userText.String()})
		}
	default:
		if userText.Len() > 0 || assistantText.Len() > 0 {
			out = append(out, openAIMessage{Role: m.Role, Content: userText.String() + assistantText.String()})
		}
	}

	if len(out) == 0 {
		out = append(out, openAIMessage{Role: m.Role, Content: ""})
	}
	return out, nil
}

// translateToolChoice maps Anthropic tool_choice to OpenAI tool_choice.
func translateToolChoice(raw json.RawMessage) json.RawMessage {
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return raw
	}
	switch tc.Type {
	case "auto":
		return json.RawMessage(`"auto"`)
	case "any":
		return json.RawMessage(`"required"`)
	case "tool":
		if tc.Name != "" {
			b, _ := json.Marshal(map[string]any{
				"type": "function",
				"function": map[string]any{"name": tc.Name},
			})
			return b
		}
		return json.RawMessage(`"auto"`)
	case "none":
		return json.RawMessage(`"none"`)
	}
	return raw
}

// extractText pulls a plain-text value from a system field that may be a
// string or an array of {type:text,text} blocks.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" || s == "" {
		return ""
	}
	if raw[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			return str
		}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" || blk.Type == "" {
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

// flattenResultContent turns a tool_result content (string or array of text
// blocks) into a single string for the OpenAI "tool" message.
func flattenResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			b.WriteString(blk.Text)
		}
		return b.String()
	}
	return strings.Trim(string(raw), `"`)
}

// jsonString unmarshals a RawMessage that is expected to be a JSON string.
func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}