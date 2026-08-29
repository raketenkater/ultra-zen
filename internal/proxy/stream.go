package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// streamIdleTimeout caps how long the relay waits between lines from the
// upstream SSE stream. There is deliberately no overall http.Client timeout
// (streams can legitimately run for many minutes), so without an inter-chunk
// deadline a gateway that accepts the request and then goes silent hangs the
// turn until Claude Code's own cancel lands — and the resulting
// canceled-context error is misattributed to rate limiting, reordering the
// pool with a bogus "every available route is throttled". Declared as a var so
// tests can shrink it.
var streamIdleTimeout = 120 * time.Second

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
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamLine is one scanner result relayed from the reader goroutine to the
// main translate loop, so the loop can select the line stream against the
// idle watchdog instead of blocking inside scanner.Scan().
type streamLine struct {
	text string
	err  error
}

// streamState tracks the Anthropic event sequence we are emitting.
type streamState struct {
	w          http.ResponseWriter
	flusher    http.Flusher
	model      string
	started    bool
	blockIndex int
	textOpen   bool
	// textBlocks counts opened text blocks so completion logging can report
	// what the relay actually handed the client.
	textBlocks int
	// toolBlocks/toolStarted are keyed by tool id (not openai index): some
	// providers reuse index 0 for each new tool call, so keying by index would
	// collapse a second subagent spawn into the first block and the agent
	// overview would show one agent instead of many.
	toolBlocks  map[string]int // tool id -> anthropic block index
	toolStarted map[string]bool
	// idForIndex remembers the last tool id announced at each OpenAI stream
	// index. The standard OpenAI shape sends id+name once and then bare
	// argument fragments at the same index; without this, those fragments
	// would key to a different bucket and open a phantom block.
	idForIndex map[int]string
	// toolOrder is toolBlocks' keys in creation order, so content_block_stop
	// events are emitted deterministically in block-index order.
	toolOrder []string
	finish    string
	output    int
	// sawDone tracks whether an explicit [DONE] sentinel was consumed from
	// the upstream stream. Its absence at end-of-input, combined with an empty
	// finish reason, distinguishes a genuine upstream completion from a
	// vanished connection (gateway cut, LB timeout) so the latter can be
	// surfaced as an error instead of a half-finished answer masquerading as
	// "end_turn".
	sawDone bool
	// reasoning buffers reasoning_content deltas. Reasoning models (glm-5.2,
	// deepseek, kimi) may emit their whole answer in reasoning_content with
	// content staying empty. We hold it and only surface it at stream end if no
	// real content arrived — mirroring the non-stream path, where content wins
	// and reasoning is the fallback so Claude Code is never handed an empty turn.
	reasoning   strings.Builder
	emittedText bool
}

// hasContent reports whether the upstream delivered anything worth handing to
// the client: real text, an opened tool block, or buffered reasoning.
func (s *streamState) hasContent() bool {
	return s.emittedText || len(s.toolStarted) > 0 || s.reasoning.Len() > 0
}

// streamTranslate reads OpenAI SSE from upstream and writes Anthropic SSE to
// the client. It owns the ResponseWriter for the lifetime of the stream. A
// non-nil error means the turn did not complete — the client has already been
// sent an SSE error event, so it can surface and retry the turn instead of
// silently stopping.
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

	// The watchdog cannot select on a blocking scanner.Scan(), so the byte
	// reader lives in a goroutine that hands up whole lines. done closes when
	// this function returns; the select in the sender lets the goroutine exit
	// on abandonment (a stalled Close below additionally unblocks a reader
	// parked in Read).
	lines := make(chan streamLine)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			select {
			case lines <- streamLine{text: scanner.Text()}:
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- streamLine{err: err}:
			case <-done:
			}
		}
	}()

	idle := time.NewTimer(streamIdleTimeout)
	defer idle.Stop()

	for {
		var (
			sline streamLine
			ok    bool
		)
		select {
		case sline, ok = <-lines:
			if !ok {
				// A clean EOF that carries neither the [DONE] sentinel nor any
				// finish_reason chunk means the upstream vanished mid-generation
				// (gateway cut, LB idle timeout) rather than completing. Emit an
				// Anthropic error event so Claude Code treats the turn as failed
				// and retries, instead of accepting a half-written answer as a
				// finished "end_turn".
				if !st.sawDone && st.finish == "" {
					st.abortStream("upstream stream ended prematurely (no finish_reason, no [DONE])")
					return io.ErrUnexpectedEOF
				}
				return st.finishStream()
			}
		case <-idle.C:
			// Silence longer than the cap: the gateway is holding the
			// connection open without generating. Aborting here beats waiting
			// for Claude Code's cancel, whose canceled-context error would run
			// the transport-error path and fake a throttle. Close the body so
			// the reader goroutine's parked Read unblocks.
			st.abortStream(fmt.Sprintf("upstream stream stalled: no data from %s for %v", model, streamIdleTimeout))
			if closer, isCloser := body.(io.Closer); isCloser {
				closer.Close()
			}
			return fmt.Errorf("upstream stream stalled: no data from %s for %v", model, streamIdleTimeout)
		}
		// Any line from upstream — data chunk, SSE keepalive comment, blank
		// event separator — proves the connection is alive; reset the watchdog.
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(streamIdleTimeout)

		if sline.err != nil {
			st.abortStream(fmt.Sprintf("upstream connection failed mid-stream: %v", sline.err))
			return sline.err
		}
		line := sline.text
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			st.sawDone = true
			return st.finishStream()
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A malformed data: line means content the provider sent is being
			// dropped on the floor — the same silent-truncation family as an
			// unterminated stream. Surface it in the log instead of swallowing
			// it, so a provider emitting bad frames is diagnosable.
			log.Printf("ultra-zen proxy stream: skipping malformed chunk: %v", err)
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
}

// abortStream terminates a broken relay with an Anthropic SSE error event.
// Headers and possibly content deltas have already been written, so the HTTP
// status cannot change; the error event is the only channel left to tell the
// client the turn failed. message_stop is deliberately omitted — the message
// never completed, and emitting it would let the SDK accept the truncation.
func (s *streamState) abortStream(msg string) {
	s.writeEvent("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": msg},
	})
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
		s.textBlocks++
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

// finishStream closes a protocol-complete stream. It returns an error when the
// stream carried nothing at all; the relay then surfaces the failure instead of
// fabricating a successful turn.
func (s *streamState) finishStream() error {
	if !s.hasContent() {
		// A protocol end ([DONE] or a finish_reason chunk) that carries zero
		// content used to be completed as a fabricated empty end_turn — a
		// perfectly-formed, perfectly silent assistant message. Claude Code
		// rendered it as "the model stopped mid-task", the classifier could
		// not see it (any data:-framed prefix is bodyOK upstream), and no
		// retry ever happened. Abort instead: the SSE error event plus the
		// returned error make this a visible, retryable failure, matching how
		// the non-stream path already rotates past an empty completion.
		s.abortStream(fmt.Sprintf("upstream_no_content: %s ended the stream without any text, tool call, or reasoning", s.model))
		return fmt.Errorf("upstream_no_content from %s (protocol-complete stream with zero content)", s.model)
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
	// The guard must NOT fire on a genuine length/max_tokens finish: there the
	// tool's input_json_delta is truncated mid-JSON, and relabeling the turn
	// "tool_use" makes Claude Code execute that partial input. Keeping
	// "max_tokens" instead routes the turn through the client's
	// retry/continuation logic, which is the honest answer for a truncated
	// generation.
	truncated := s.finish == "length" || s.finish == "max_tokens"
	if len(s.toolStarted) > 0 && stop != "tool_use" && !truncated {
		stop = "tool_use"
	}
	// One line per completed stream: enough to reconstruct what the client saw
	// (and to spot the empty-ish turns) without dumping content on every turn.
	log.Printf("ultra-zen proxy stream: completed model=%s finish_reason=%q stop_reason=%s text_blocks=%d tool_blocks=%d output_tokens=%d",
		s.model, s.finish, stop, s.textBlocks, len(s.toolStarted), s.output)
	s.writeEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": s.output},
	})
	s.writeEvent("message_stop", map[string]any{"type": "message_stop"})
	return nil
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
