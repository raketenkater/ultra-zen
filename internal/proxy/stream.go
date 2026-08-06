package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// streamChunk is one OpenAI streaming chunk.
type streamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string       `json:"role"`
			Content   string       `json:"content"`
			ToolCalls []streamTool `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type streamTool struct {
	Index    int `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamState tracks the Anthropic event sequence we are emitting.
type streamState struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	model       string
	started     bool
	blockIndex  int
	textOpen    bool
	toolBlocks  map[int]int  // openai tool index -> anthropic block index
	toolStarted map[int]bool
	finish      string
	output      int
}

// streamTranslate reads OpenAI SSE from upstream and writes Anthropic SSE to
// the client. It owns the ResponseWriter for the lifetime of the stream.
func streamTranslate(w http.ResponseWriter, body io.Reader, model string) error {
	flusher, _ := w.(http.Flusher)
	st := &streamState{
		w:           w,
		flusher:     flusher,
		model:       model,
		toolBlocks:  make(map[int]int),
		toolStarted: make(map[int]bool),
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			st.finishStream()
			return nil
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			st.output = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		st.ensureStarted(&chunk)
		if choice.Delta.Content != "" {
			st.emitText(choice.Delta.Content)
		}
		for _, tc := range choice.Delta.ToolCalls {
			st.emitToolDelta(tc)
		}
		if choice.FinishReason != "" {
			st.finish = choice.FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	st.finishStream()
	return nil
}

func (s *streamState) ensureStarted(chunk *streamChunk) {
	if s.started {
		return
	}
	s.started = true
	id := "msg_" + chunk.ID
	if id == "msg_" {
		id = "msg_ultra-zen"
	}
	start := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         s.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}
	s.writeEvent("message_start", start)
}

func (s *streamState) emitText(text string) {
	if !s.textOpen {
		s.writeEvent("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         s.blockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		s.textOpen = true
	}
	s.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.blockIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

func (s *streamState) emitToolDelta(tc streamTool) {
	// Close the text block (if open) before starting a tool block: Anthropic
	// content blocks must be sequential and non-overlapping.
	if s.textOpen {
		s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.blockIndex})
		s.blockIndex++
		s.textOpen = false
	}
	if !s.toolStarted[tc.Index] {
		s.toolStarted[tc.Index] = true
		s.toolBlocks[tc.Index] = s.blockIndex
		s.writeEvent("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": s.blockIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": map[string]any{},
			},
		})
	}
	if tc.Function.Arguments != "" {
		s.writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": s.toolBlocks[tc.Index],
			"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
		})
	}
}

func (s *streamState) finishStream() {
	if !s.started {
		// Upstream produced nothing usable; emit a minimal valid stream.
		s.ensureStarted(&streamChunk{})
		s.emitText("")
	}
	if s.textOpen {
		s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.blockIndex})
		s.textOpen = false
		s.blockIndex++
	}
	for idx, started := range s.toolStarted {
		if started {
			s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.toolBlocks[idx]})
		}
	}
	stop := mapStopReason(s.finish)
	if stop == "" {
		stop = "end_turn"
	}
	// Tool-block-aware stop_reason: if the upstream emitted tool_use blocks but
	// never delivered a final finish_reason chunk (some gateways end with a
	// usage-only chunk or just [DONE]), force stop_reason to "tool_use".
	// Otherwise Claude Code treats the turn as finished and never executes the
	// pending tool call — breaking subagent spawn (agent overview) and MCP
	// research calls. The same guard applies when a gateway sends
	// finish_reason="stop" despite emitting tool_calls.
	if len(s.toolStarted) > 0 && stop != "tool_use" {
		stop = "tool_use"
	}
	s.writeEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": s.output},
	})
	s.writeEvent("message_stop", map[string]any{"type": "message_stop"})
}

// writeEvent writes one Anthropic SSE event and flushes.
func (s *streamState) writeEvent(event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, payload)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}