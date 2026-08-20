// Package proxy is the Anthropic-to-OpenAI translation bridge. Claude Code
// speaks the Anthropic Messages API; the opencode Zen gateway speaks OpenAI
// Chat Completions. This package runs an HTTP server (started as a goroutine
// by the launcher) that accepts /v1/messages, translates the request, forwards
// it to the Zen gateway, and translates the response back — including SSE
// streaming and tool-calling.
package proxy

import (
	"bytes"
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
	Thinking      json.RawMessage `json:"thinking"` // extended thinking config — stripped, not forwarded
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
	Role             string       `json:"role"`
	Content          any          `json:"content,omitempty"`
	ReasoningContent *string      `json:"reasoning_content,omitempty"`
	ToolCalls        []openAITool `json:"tool_calls,omitempty"`
	ToolCallID       string       `json:"tool_call_id,omitempty"`
	Name             string       `json:"name,omitempty"`
}

type openAITool struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"` // "function"
	Function openAIToolFunc `json:"function"`
}

type openAIToolFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
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

// clone returns a request whose message slice can be fitted for one upstream
// without progressively truncating the pristine request used by later routes.
func (r *openAIRequest) clone() *openAIRequest {
	c := *r
	c.Messages = append([]openAIMessage(nil), r.Messages...)
	return &c
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
	// Also scan the conversation history for tool_calls referencing tools that
	// aren't in the current tool list (e.g., a tool the model tried to use in
	// a previous turn but isn't available in this session). The upstream
	// provider rejects tool_calls for unknown tools, so we add minimal stubs.
	seenTools := map[string]bool{}
	tools := make([]map[string]any, 0, len(a.Tools))
	for _, t := range a.Tools {
		seenTools[t.Name] = true
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
	// Scan message history for tools referenced in tool_calls.
	for _, m := range a.Messages {
		var blocks []map[string]json.RawMessage
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if jsonString(b["type"]) == "tool_use" {
				name := jsonString(b["name"])
				if name != "" && !seenTools[name] {
					seenTools[name] = true
					tools = append(tools, map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": name,
							"parameters": map[string]any{
								"type":       "object",
								"properties": map[string]any{},
								"required":   []string{},
							},
						},
					})
				}
			}
		}
	}
	if len(tools) > 0 {
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

	req.Messages = sanitizeToolMessages(req.Messages)
	req.Messages = repairUnresolvedToolCalls(req.Messages)

	return req, nil
}

// truncateToContext fits a request into the model window without silently
// destroying useful history. It first reduces the requested output allowance;
// Claude Code commonly asks for 65k even though an ordinary turn needs far less.
// Only when the input itself has passed the emergency threshold does it remove
// complete oldest conversation turns. Tool-call/result sequences are therefore
// never split at the truncation boundary, and an explicit system note tells the
// model that history is missing.
//
// Claude Code normally compacts at 85% of the advertised window. The adaptive
// safety and minimum-output reserves below put destructive trimming above 90%,
// making this a last resort for a compaction request that would otherwise be
// rejected rather than something that runs before normal compaction.
func (r *openAIRequest) truncateToContext(window, maxTokens int) string {
	if window <= 0 {
		return ""
	}

	safety := minInt(4096, maxInt(512, window/32))
	minimumOutput := minInt(8192, maxInt(1024, window/20))
	input := r.estimatedInputTokens()
	availableOutput := window - safety - input
	if availableOutput >= minimumOutput {
		if maxTokens > availableOutput {
			r.MaxTokens = availableOutput
			return fmt.Sprintf("reduced max_tokens from %d to %d to preserve the complete conversation", maxTokens, availableOutput)
		}
		return ""
	}

	// Preserve leading system messages and the newest user-led turn. Each cut is
	// made immediately before a later user message, so assistant tool_calls and
	// their following tool results remain together on one side of the boundary.
	systemEnd := 0
	for systemEnd < len(r.Messages) && r.Messages[systemEnd].Role == "system" {
		systemEnd++
	}
	trimmed := 0
	for input > window-safety-minimumOutput {
		cut := -1
		for i := systemEnd + 1; i < len(r.Messages); i++ {
			if r.Messages[i].Role == "user" {
				cut = i
				break
			}
		}
		if cut < 0 {
			break // one oversized current turn: do not corrupt its tool sequence
		}
		trimmed += cut - systemEnd
		r.Messages = append(r.Messages[:systemEnd], r.Messages[cut:]...)
		input = r.estimatedInputTokens()
	}

	if trimmed > 0 {
		r.addContextRescueNote(trimmed)
		// Be defensive if an unusual message shape crossed a user boundary.
		r.Messages = repairUnresolvedToolCalls(sanitizeToolMessages(r.Messages))
		input = r.estimatedInputTokens()
	}
	availableOutput = window - safety - input
	if availableOutput < 1 {
		availableOutput = 1
	}
	if r.MaxTokens > availableOutput {
		r.MaxTokens = availableOutput
	}
	if trimmed == 0 {
		return fmt.Sprintf("current turn is too large for the %d-token context window; preserved it intact and reduced max_tokens to %d", window, r.MaxTokens)
	}
	return fmt.Sprintf("removed %d message(s) in complete old turn(s) to fit the %d-token context window; max_tokens=%d", trimmed, window, r.MaxTokens)
}

// estimatedInputTokens counts the complete serialized prompt, including tool
// schemas and tool choice. The old estimator ignored both and assumed four
// characters per token; JSON, source code, and non-English text often tokenize
// more densely, so three bytes per token plus fixed framing is safer.
func (r *openAIRequest) estimatedInputTokens() int {
	prompt := struct {
		Messages   []openAIMessage `json:"messages"`
		Tools      json.RawMessage `json:"tools,omitempty"`
		ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	}{r.Messages, r.Tools, r.ToolChoice}
	b, err := json.Marshal(prompt)
	if err != nil {
		return 1024
	}
	return (len(b)+2)/3 + 256
}

func (r *openAIRequest) addContextRescueNote(trimmed int) {
	note := fmt.Sprintf("[ultra-zen context rescue: %d older message(s) were omitted as complete turns because the request exceeded the model context window. Do not invent missing details; re-read repository files or ask the user when they matter.]", trimmed)
	for i := range r.Messages {
		if r.Messages[i].Role != "system" {
			break
		}
		if s, ok := r.Messages[i].Content.(string); ok {
			r.Messages[i].Content = s + "\n\n" + note
			return
		}
	}
	r.Messages = append([]openAIMessage{{Role: "system", Content: note}}, r.Messages...)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// sanitizeToolMessages guarantees every "tool" message is a well-formed answer
// to a tool_call that was actually announced earlier in the conversation.
//
// Two malformed shapes reach us from Claude Code and both make the gateway's
// upstream provider reject the whole request:
//
//   - A tool_result block with a missing or empty tool_use_id. ToolCallID is
//     omitempty, so it serializes as a "tool" message with no tool_call_id at
//     all and the provider fails to deserialize the body:
//     `messages[N]: missing field tool_call_id`.
//   - A tool_result whose id matches no preceding assistant tool_call (history
//     compaction dropped the assistant turn, or an agent loop was interrupted).
//
// Both are repaired by adopting the oldest still-unanswered tool_call id from
// the most recent assistant tool_calls turn. If there is nothing to answer, the
// result is demoted to a plain user message so its content is not lost.
func sanitizeToolMessages(msgs []openAIMessage) []openAIMessage {
	announced := map[string]bool{} // every tool_call id seen so far
	var pending []string           // unanswered ids from the latest assistant turn

	out := make([]openAIMessage, 0, len(msgs))
	for i, m := range msgs {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			calls := make([]openAITool, len(m.ToolCalls))
			copy(calls, m.ToolCalls)
			pending = pending[:0]
			for j := range calls {
				if calls[j].ID == "" {
					calls[j].ID = fmt.Sprintf("call_%d_%d", i, j)
				}
				announced[calls[j].ID] = true
				pending = append(pending, calls[j].ID)
			}
			m.ToolCalls = calls
			out = append(out, m)

		case m.Role == "tool":
			if m.ToolCallID == "" || !announced[m.ToolCallID] {
				if len(pending) == 0 {
					// Nothing to answer — an orphan result. Keep the text as
					// user content rather than emitting an invalid tool turn.
					out = append(out, openAIMessage{Role: "user", Content: contentString(m.Content)})
					continue
				}
				m.ToolCallID = pending[0]
			}
			pending = dropID(pending, m.ToolCallID)
			out = append(out, m)

		default:
			out = append(out, m)
		}
	}
	return out
}

// dropID removes the first occurrence of id from ids.
func dropID(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			return append(ids[:i:i], ids[i+1:]...)
		}
	}
	return ids
}

// contentString renders an openAIMessage content value as plain text.
func contentString(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.RawMessage:
		return string(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// repairUnresolvedToolCalls makes every assistant tool_call round-trip
// complete. The Zen gateway's upstream provider rejects an assistant message
// carrying tool_calls unless each call is answered by a matching "tool"
// result message later in the conversation — a dangling tool_calls turn (e.g.
// after history compaction, or an interrupted agent loop) gets a 400
// "Upstream request failed". For every tool_call id with no matching tool
// message, we insert a stub tool result immediately after that assistant
// message. Existing tool results are left untouched; inserting the stubs
// directly after the assistant message keeps the required
// assistant(tool_calls) -> tool* ordering.
func repairUnresolvedToolCalls(msgs []openAIMessage) []openAIMessage {
	// Collect every tool_call_id that already has a tool result message.
	resolved := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			resolved[m.ToolCallID] = true
		}
	}

	out := make([]openAIMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if resolved[tc.ID] {
				continue
			}
			id := tc.ID
			if id == "" {
				id = "toolu_stub"
			}
			out = append(out, openAIMessage{
				Role:       "tool",
				ToolCallID: id,
				Content:    "(tool result unavailable)",
			})
			resolved[id] = true // don't stub the same id twice
		}
	}
	return out
}

// translateMessage turns one Anthropic message into one or more OpenAI messages.
// A user message containing tool_result blocks emits separate "tool" messages
// (one per result) followed by an optional "user" message for any text.
func translateMessage(m anthropicMsg) ([]openAIMessage, error) {
	// Normalize the role for OpenAI: the only valid roles in the messages
	// array are "user", "assistant", and "tool". Any non-standard role
	// (e.g. "system" — mid-conversation system injections from Claude
	// Code) maps to "user" to avoid duplicate system messages which the
	// upstream provider rejects with a 400 "Upstream request failed".
	role := m.Role
	if role != "user" && role != "assistant" {
		role = "user"
	}

	content := strings.TrimSpace(string(m.Content))
	// String content.
	if len(m.Content) == 0 || content == `""` || content == "null" {
		return []openAIMessage{{Role: role, Content: ""}}, nil
	}
	// If content does not start with '[', it is a plain string.
	if m.Content[0] != '[' {
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			// Not a string; treat as opaque content.
			return []openAIMessage{{Role: role, Content: json.RawMessage(m.Content)}}, nil
		}
		return []openAIMessage{{Role: role, Content: s}}, nil
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
			if role == "assistant" {
				assistantText.WriteString(txt)
			} else {
				userText.WriteString(txt)
			}
		case "thinking":
			// Extended-thinking blocks (Claude Code --effort max). The Zen
			// gateway has no thinking channel, so fold the thinking text into
			// the assistant content. This also prevents an empty assistant
			// message (thinking-only) which providers reject with a 400.
			txt := jsonString(b["thinking"])
			if txt == "" {
				txt = jsonString(b["text"])
			}
			if txt != "" {
				assistantText.WriteString(txt)
			}
		case "tool_use":
			id := jsonString(b["id"])
			name := jsonString(b["name"])
			// A tool_use with absent or non-object input marshals to "null",
			// which providers reject as tool arguments; send {} instead.
			input := []byte(`{}`)
			if raw := bytes.TrimSpace(b["input"]); len(raw) > 0 && raw[0] == '{' && json.Valid(raw) {
				input = raw
			}
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

	switch role {
	case "assistant":
		msg := openAIMessage{Role: "assistant"}
		if assistantText.Len() > 0 {
			msg.Content = assistantText.String()
		}
		if len(assistantToolCalls) > 0 {
			msg.ToolCalls = assistantToolCalls
			// DeepSeek requires reasoning_content on every assistant message
			// that carries tool_calls, even if only an empty placeholder.
			// Without it the provider rejects the request with 400 "Upstream
			// request failed" (param:null).
			empty := ""
			msg.ReasoningContent = &empty
		}
		out = append(out, msg)
	case "user":
		// tool messages were already appended; add trailing user text if any.
		if userText.Len() > 0 {
			out = append(out, openAIMessage{Role: "user", Content: userText.String()})
		}
	default:
		// role was normalized to "user" at the top for non-standard roles.
		if userText.Len() > 0 || assistantText.Len() > 0 {
			out = append(out, openAIMessage{Role: role, Content: userText.String() + assistantText.String()})
		}
	}

	if len(out) == 0 {
		out = append(out, openAIMessage{Role: role, Content: ""})
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
				"type":     "function",
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
