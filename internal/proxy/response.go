package proxy

import (
	"encoding/json"
	"strings"
)

// openAIResponse is the non-streaming Chat Completions response from the gateway.
type openAIResponse struct {
	ID      string         `json:"id"`
	Choices []openAIChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// openAIChoice is one choice in a non-streaming Chat Completions response.
type openAIChoice struct {
	Index   int `json:"index"`
	Message struct {
		Role      string       `json:"role"`
		Content   string       `json:"content"`
		ToolCalls []openAITool `json:"tool_calls"`
		Reasoning string       `json:"reasoning_content"` // stripped, never forwarded
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// anthropicContentBlock is one block in an Anthropic message content array.
type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// anthropicResponse is the Anthropic Messages response we return to Claude Code.
type anthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"` // "message"
	Role         string                  `json:"role"` // "assistant"
	Model        string                  `json:"model"`
	Content      []anthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// toAnthropic translates a non-streaming OpenAI response into an Anthropic
// Messages response. model is the model id Claude Code expects to see echoed.
func (r *openAIResponse) toAnthropic(model string) *anthropicResponse {
	resp := &anthropicResponse{
		ID:    "msg_" + r.ID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
		Usage: anthropicUsage{
			InputTokens:  r.Usage.PromptTokens,
			OutputTokens: r.Usage.CompletionTokens,
		},
	}
	if len(r.Choices) == 0 {
		resp.StopReason = "end_turn"
		return resp
	}
	choice := r.Choices[0]
	resp.StopReason = mapStopReason(choice.FinishReason)
	// Tool-block-aware stop_reason: when the gateway emitted tool_calls but the
	// finish_reason is missing/"stop", Claude Code must still see stop_reason
	// "tool_use" or it will never execute the pending tool call (breaking
	// subagent spawn / MCP research). This mirrors the stream path. A genuine
	// length/max_tokens finish is the exception there and here: the arguments
	// are truncated (toolInput degrades them to {}), so keep "max_tokens" and
	// let Claude Code retry instead of executing a partial tool call.
	truncated := choice.FinishReason == "length" || choice.FinishReason == "max_tokens"
	if len(choice.Message.ToolCalls) > 0 && resp.StopReason != "tool_use" && !truncated {
		resp.StopReason = "tool_use"
	}

	if choice.Message.Content != "" {
		resp.Content = append(resp.Content, anthropicContentBlock{Type: "text", Text: choice.Message.Content})
	} else if choice.Message.Reasoning != "" {
		// Reasoning models (e.g. glm-5.1) may emit the answer in
		// reasoning_content when the token budget is exhausted. Surface it as a
		// text block so Claude Code is never handed an empty message.
		resp.Content = append(resp.Content, anthropicContentBlock{Type: "text", Text: choice.Message.Reasoning})
	}
	for _, tc := range choice.Message.ToolCalls {
		// arguments is a JSON string; splice it in as the Anthropic input
		// object. A model that hit max_tokens mid-arguments leaves it
		// truncated ({"cmd": "l), and embedding that raw makes json.Marshal of
		// the whole response fail — the client then gets an empty body.
		input := toolInput(tc.Function.Arguments)
		resp.Content = append(resp.Content, anthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	if len(resp.Content) == 0 {
		resp.Content = []anthropicContentBlock{{Type: "text", Text: ""}}
	}
	return resp
}

// toolInput turns an OpenAI tool-call arguments string into an Anthropic
// tool_use input object. Anything that is not a valid JSON object — empty,
// truncated, "null", or a bare scalar — degrades to {} so the response stays
// serializable and the client still sees the tool call.
func toolInput(args string) json.RawMessage {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed[0] != '{' || !json.Valid([]byte(trimmed)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}

// mapStopReason converts OpenAI finish_reason to Anthropic stop_reason.
func mapStopReason(finish string) string {
	switch finish {
	case "stop":
		return "end_turn"
	case "tool_calls", "function_call":
		return "tool_use"
	case "length", "max_tokens":
		// Some gateways echo the Anthropic spelling "max_tokens" as the raw
		// finish_reason instead of OpenAI's "length"; both mean the token
		// budget cut the turn, and Claude Code must see max_tokens so it
		// retries rather than treating the truncation as a finished turn.
		return "max_tokens"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}
