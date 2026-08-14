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
			Role             string       `json:"role"`
			Content          string       `json:"content"`
			ReasoningContent string       `json:"reasoning_content"`
			ToolCalls        []streamTool `json:"tool_calls"`
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
	// toolBlocks/toolStarted are keyed by tool id (not openai index): some
	// providers reuse index 0 for each new tool call, so keying by index would
	// collapse a second subagent spawn into the first block and the agent
	// overview would show one agent instead of many.
	toolBlocks  map[string]int  // tool id -> anthropic block index
	toolStarted map[string]bool
	// idForIndex remembers the last tool id announced at each OpenAI stream
	// index. The standard OpenAI shape sends id+name once and then bare
	// argument fragments at the same index; without this, those fragments
	// would key to a different bucket and open a phantom block.
	idForIndex map[int]string
	// toolOrder is toolBlocks' keys in creation order, so content_block_stop
	// events are emitted deterministically in block-index order.
	toolOrder []string
	finish      string
	output      int
	// reasoning buffers reasoning_content deltas. Reasoning models (glm-5.2,
	// deepseek, kimi) may emit their whole answer in reasoning_content with
	// content staying empty. We hold it and only surface it at stream end if no
	// real content arrived — mirroring the non-stream path, where content wins
	// and reasoning is the fallback so Claude Code is never handed an empty turn.
	reasoning strings.Builder
	emittedText bool
}

// streamTranslate reads OpenAI SSE from upstream and writes Anthropic SSE to
// the client. It owns the ResponseWriter for the lifetime of the stream.
func streamTranslate(w http.ResponseWriter, body io.Reader, model string) error {
	flusher, _ := w.(http.Flusher)
	st := &streamState{
		w:           w,
		flusher:     flusher,
		model:       model,
		toolBlocks:  make(map[string]int),
		toolStarted: make(map[string]bool),
		idForIndex:  make(map[int]string),
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
			st.emittedText = true
			st.emitText(choice.Delta.Content)
		} else if choice.Delta.ReasoningContent != "" {
			// Reasoning content isn't the real answer when content eventually
			// arrives; buffer it as the fallback for a reasoning-only stream.
			st.reasoning.WriteString(choice.Delta.ReasoningContent)
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
	// Key tool blocks by id, not index. OpenAI providers may announce multiple
	// tool calls in one delta (indices 0..N) or reuse index 0 for each new call
	// in a later chunk; keying by index would collapse distinct subagent spawns
	// into one block and the agent overview would show one agent instead of many.
	// Resolve the id: a chunk that carries one (re)binds the stream index; a
	// bare argument-fragment chunk inherits the id last bound to its index.
	// Falling straight through to an index-derived key here would open a
	// second, phantom tool block with an empty id and name for every argument
	// fragment — and Claude Code would then answer it with a tool_result whose
	// tool_use_id is empty, which the gateway rejects outright.
	id := tc.ID
	if id != "" {
		s.idForIndex[tc.Index] = id
	} else if prev, ok := s.idForIndex[tc.Index]; ok {
		id = prev
	} else {
		// A tool call whose id the provider never sends at all. Synthesize a
		// stable one so the tool_use block Claude Code sees is answerable.
		id = fmt.Sprintf("toolu_idx%d", tc.Index)
		s.idForIndex[tc.Index] = id
	}
	key := id
	if !s.toolStarted[key] {
		s.toolStarted[key] = true
		s.toolOrder = append(s.toolOrder, key)
		// Advance the block index for this new tool block. Without this, two
		// tool_use blocks emitted back-to-back (no intervening text block) both
		// get the same index — Anthropic requires sequential, unique content
		// block indices, and Claude Code keys its agent-overview tracking on
		// block id + index.
		s.toolBlocks[key] = s.blockIndex
		s.blockIndex++
		s.writeEvent("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": s.toolBlocks[key],
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  tc.Function.Name,
				"input": map[string]any{},
			},
		})
	}
	if tc.Function.Arguments != "" {
		s.writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": s.toolBlocks[key],
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
	// Reasoning-only stream: no real content and no tool calls arrived, but the
	// model answered in reasoning_content. Surface it as text so Claude Code
	// isn't handed an empty assistant turn.
	if !s.emittedText && len(s.toolStarted) == 0 && s.reasoning.Len() > 0 {
		s.emitText(s.reasoning.String())
	}
	if s.textOpen {
		s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.blockIndex})
		s.textOpen = false
		s.blockIndex++
	}
	// Close in creation order: map iteration would emit content_block_stop
	// events in a random order from one run to the next.
	for _, key := range s.toolOrder {
		s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.toolBlocks[key]})
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