package proxy

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

var errForced = errors.New("forced upstream failure")

// errReader always fails, simulating an upstream connection that breaks
// mid-stream.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errForced }

// relayOutput drives streamTranslate over an in-memory upstream and returns
// exactly what a downstream Anthropic SSE client would receive.
func relayOutput(upstream io.Reader) string {
	rec := httptest.NewRecorder()
	_ = streamTranslate(rec, upstream, "test-model")
	return rec.Body.String()
}

func hasEvent(raw, name string) bool {
	return strings.Contains(raw, "event: "+name+"\n")
}

// TestStreamAbortOnUpstreamError: an upstream connection failure mid-generation
// must reach the client as an Anthropic SSE error event — not as a silent
// stream end, which Claude Code renders as the model simply stopping
// mid-sentence.
func TestStreamAbortOnUpstreamError(t *testing.T) {
	up := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial ans\"}}]}\n\n"
	raw := relayOutput(io.MultiReader(strings.NewReader(up), errReader{}))
	if !hasEvent(raw, "error") {
		t.Fatalf("relay missing error event on upstream failure:\n%s", raw)
	}
	if hasEvent(raw, "message_stop") {
		t.Fatalf("error relay must not emit message_stop (truncation must not look complete):\n%s", raw)
	}
}

// TestStreamAbortOnPrematureEOF: a clean EOF without [DONE] and without any
// finish_reason chunk is a vanished upstream, not a completed answer. The relay
// must surface it as an error event rather than fabricating stop_reason
// "end_turn" over half-generated text.
func TestStreamAbortOnPrematureEOF(t *testing.T) {
	raw := relayOutput(strings.NewReader(
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half thought\"}}]}\n\n",
	))
	if !hasEvent(raw, "error") {
		t.Fatalf("premature EOF missing error event:\n%s", raw)
	}
	if hasEvent(raw, "message_delta") || hasEvent(raw, "message_stop") {
		t.Fatalf("premature EOF must not fabricate completion events:\n%s", raw)
	}
	if !strings.Contains(raw, "api_error") {
		t.Fatalf("error event should carry api_error type:\n%s", raw)
	}
}

// TestStreamCleanCompletionStillWorks guards against false positives: a normal
// gateway stream ([DONE], finish_reason present) must still terminate cleanly.
func TestStreamCleanCompletionStillWorks(t *testing.T) {
	raw := relayOutput(strings.NewReader(
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"all done\"}}]}\n\n" +
			"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
	))
	if hasEvent(raw, "error") {
		t.Fatalf("clean completion wrongly aborted:\n%s", raw)
	}
	if !hasEvent(raw, "message_stop") {
		t.Fatalf("clean completion missing message_stop:\n%s", raw)
	}
	if !strings.Contains(raw, `"end_turn"`) {
		t.Fatalf("clean completion stop_reason = want end_turn:\n%s", raw)
	}
}

// TestStreamDoneWithoutFinishReasonIsCompletion: some gateways end with [DONE]
// but never send a finish_reason chunk. The sentinel itself signals protocol
// completion, so this must NOT be treated as truncation.
func TestStreamDoneWithoutFinishReasonIsCompletion(t *testing.T) {
	raw := relayOutput(strings.NewReader(
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"text\"}}]}\n\n" +
			"data: [DONE]\n\n",
	))
	if hasEvent(raw, "error") {
		t.Fatalf("[DONE]-terminated stream wrongly aborted:\n%s", raw)
	}
	if !hasEvent(raw, "message_stop") {
		t.Fatalf("[DONE]-terminated stream missing message_stop:\n%s", raw)
	}
}

// TestResponsesSSEErrorPropagates: when the Responses-API upstream breaks
// mid-stream, responsesSSEStream must propagate the scanner error through the
// pipe instead of writing a masking [DONE].
func TestResponsesSSEErrorPropagates(t *testing.T) {
	sse := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n"
	out := responsesSSEStream(context.Background(), io.MultiReader(strings.NewReader(sse), errReader{}))
	data, err := io.ReadAll(out)
	if err == nil {
		t.Fatalf("pipe error swallowed; got clean EOF, data=%q", data)
	}
	if !errors.Is(err, errForced) {
		t.Fatalf("propagated error = %v, want errForced", err)
	}
	if strings.Contains(string(data), "[DONE]") {
		t.Fatalf("broken Responses stream must not be terminated with [DONE]: %q", string(data))
	}
}
