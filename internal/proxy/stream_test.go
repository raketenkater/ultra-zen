package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// withIdleTimeout shrinks the inter-chunk watchdog for the duration of a test.
// Tests in this package do not run in parallel, so mutating the package-level
// seam is safe.
func withIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := streamIdleTimeout
	streamIdleTimeout = d
	t.Cleanup(func() { streamIdleTimeout = prev })
}

// stallReader delivers head once, then blocks in Read forever — the shape of a
// gateway that accepted the request and stopped generating without closing the
// connection. Close unblocks the parked Read (which also releases the relay's
// reader goroutine, keeping -race/goroutine accounting clean).
type stallReader struct {
	head io.Reader
	quit chan struct{}
	once sync.Once
}

func newStallReader(head string) *stallReader {
	return &stallReader{head: strings.NewReader(head), quit: make(chan struct{})}
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.head != nil {
		n, err := r.head.Read(p)
		if err == io.EOF {
			r.head = nil
		} else {
			return n, err
		}
	}
	<-r.quit
	return 0, io.ErrClosedPipe
}

func (r *stallReader) Close() error {
	r.once.Do(func() { close(r.quit) })
	return nil
}

// pacedReader emits each chunk ~gap apart, simulating a stream whose only
// traffic is SSE keepalive comments. Every chunk is well under the watchdog
// budget, but the total silence between data frames is not — a relay that
// treats keepalives as liveness survives, one that doesn't aborts mid-flight.
type pacedReader struct {
	chunks []string
	i      int
	last   time.Time
	gap    time.Duration
}

func (p *pacedReader) Read(b []byte) (int, error) {
	for p.i < len(p.chunks) {
		if !p.last.IsZero() {
			time.Sleep(p.gap)
		}
		// Skip exhausted leading chunks instead of returning (0, nil).
		p.last = time.Now()
		n := copy(b, p.chunks[p.i])
		p.i++
		if n > 0 {
			return n, nil
		}
	}
	return 0, io.EOF
}

// Fix #1: a protocol-complete stream that carried nothing used to be closed
// out as a fabricated minimal end_turn — the silent-stop mechanism. It must
// now surface as an error event (visible to Claude Code, which retries) and a
// non-nil error from the relay.
func TestStreamEmptyDoneAbortsInsteadOfFabricating(t *testing.T) {
	for name, upstream := range map[string]string{
		"bare_done":       "data: [DONE]\n\n",
		"finish_only":     "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		"usage_only_done": "data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":0}}\n\ndata: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			err := streamTranslate(rec, strings.NewReader(upstream), "ghost-model")
			if err == nil {
				t.Fatalf("zero-content %s stream must return an error", name)
			}
			if !strings.Contains(err.Error(), "upstream_no_content") {
				t.Fatalf("relay error = %v, want upstream_no_content", err)
			}
			out := rec.Body.String()
			if !hasEvent(out, "error") {
				t.Fatalf("client got no error event:\n%s", out)
			}
			if !strings.Contains(out, "upstream_no_content") {
				t.Fatalf("error event must name the failure:\n%s", out)
			}
			if hasEvent(out, "message_delta") || hasEvent(out, "message_stop") {
				t.Fatalf("aborted stream must not fabricate completion events:\n%s", out)
			}
			if strings.Contains(out, `"end_turn"`) {
				t.Fatalf("aborted stream must not emit stop_reason end_turn:\n%s", out)
			}
		})
	}
}

// The non-stream sibling already treated "a completion with no content" as
// rotatable (choices:[] → bodyDegenerate → limitRoute + try next route — see
// TestDegenerate200TemporarilyRotates in retry_test.go), but classifyUpstreamBody
// cannot see the stream shape at all (any data:-framed prefix is bodyOK), so
// the relay was the only line of defense — and until fix #1 it fabricated a
// turn instead of failing. This test pins both halves: the classifier asymmetry
// (why the relay must check), and an end-to-end streamed request whose empty
// [DONE] now reaches Claude Code as an error, not a silent end_turn.
func TestStreamEmptyDoneEndToEndIsVisibleFailure(t *testing.T) {
	if got := classifyUpstreamBody([]byte(`{"id":"x","choices":[]}`)); got != bodyDegenerate {
		t.Fatalf("non-stream empty choices = %d, want bodyDegenerate (the shape that already rotated)", got)
	}
	if got := classifyUpstreamBody([]byte("data: [DONE]\n\n")); got != bodyOK {
		t.Fatalf("SSE-framed empty stream = %d, want bodyOK (classifier blind; relay must abort)", got)
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer up.Close()
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"r","choices":[{"message":{"role":"assistant","content":"fallback-ok"},"finish_reason":"stop"}]}`)
	}))
	defer fb.Close()

	s := New(Config{
		Provider: "opencode-go",
		BaseURL:  up.URL,
		APIKey:   "k",
		Model:    "ghost-model",
		Fallbacks: []Upstream{
			{Provider: "opencode-go", BaseURL: fb.URL, APIKey: "k", Model: "north-mini-code-free"},
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"ghost-model","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	s.handleMessages(rec, req)

	// The route that opened the stream is already committed (headers sent), so
	// rotation cannot save THIS request — but the client must see a loud error
	// event that Claude Code surfaces and retries, instead of the old silent
	// empty end_turn.
	if !hasEvent(rec.Body.String(), "error") {
		t.Fatalf("streamed empty [DONE] must reach the client as an SSE error event:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_no_content") {
		t.Fatalf("client-facing error must name upstream_no_content:\n%s", rec.Body.String())
	}
	if hasEvent(rec.Body.String(), "message_stop") {
		t.Fatalf("fabricated completion leaked to client:\n%s", rec.Body.String())
	}
}

// Fix #5: the relay must not hang forever on a gateway that stops emitting.
// With the watchdog seam injected small, a stalled body aborts through the
// same retryable error path as a connection drop.
func TestStreamIdleWatchdogAbortsStalledStream(t *testing.T) {
	withIdleTimeout(t, 100*time.Millisecond)
	rec := httptest.NewRecorder()
	start := time.Now()
	err := streamTranslate(rec, newStallReader("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first words\"}}]}\n\n"), "slow-model")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("watchdog did not fire promptly (%v elapsed)", time.Since(start))
	}
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("relay error = %v, want a stalled-stream error", err)
	}
	out := rec.Body.String()
	if !hasEvent(out, "error") || !strings.Contains(out, "stalled") {
		t.Fatalf("client got no stalled error event:\n%s", out)
	}
	if hasEvent(out, "message_stop") {
		t.Fatalf("stalled stream must not look complete:\n%s", out)
	}
	// The partial content already relayed must NOT be closed out as a finished
	// turn — the error event stands in its place.
	if strings.Contains(out, `"end_turn"`) {
		t.Fatalf("stalled partial stream fabricated end_turn:\n%s", out)
	}
}

// Keepalive comments (and blank event separators) prove liveness: they must
// reset the watchdog so a slow-but-alive gateway is never killed. Gaps here
// stay under the idle budget, but cumulative silence exceeds it — without a
// per-line reset the relay would abort.
func TestStreamWatchdogResetsOnKeepAlives(t *testing.T) {
	withIdleTimeout(t, 80*time.Millisecond)
	paced := &pacedReader{gap: 25 * time.Millisecond, chunks: []string{
		": keep-alive\n\n",
		"\n",
		": keep-alive\n\n",
		"\n",
		": keep-alive\n\n",
		"\n",
		": keep-alive\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"eventually\"}}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}}
	rec := httptest.NewRecorder()
	if err := streamTranslate(rec, paced, "slow-but-alive"); err != nil {
		t.Fatalf("keepalive stream wrongly aborted: %v\n%s", err, rec.Body.String())
	}
	if got := collectText(rec.Body.String()); got != "eventually" {
		t.Fatalf("text = %q, want the stream to complete normally", got)
	}
}

// Fix #10: a genuine length/max_tokens finish truncates the tool's
// input_json_delta mid-JSON. The old blanket tool-block override relabeled the
// turn "tool_use", so Claude Code executed the partial arguments. Only a
// MISSING or contradictory (stop) finish_reason may be forced to tool_use.
func TestStreamLengthFinishKeepsMaxTokensDespiteToolBlocks(t *testing.T) {
	sse := "data: " + `{"id":"c1","choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" +
		"data: " + `{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tu1","type":"function","function":{"name":"Bash","arguments":"{\"cmd\": \"grep -r"}}]}}]}` + "\n\n" +
		"data: " + `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	rec := httptest.NewRecorder()
	if err := streamTranslate(rec, strings.NewReader(sse), "m"); err != nil {
		t.Fatalf("truncated tool stream carries content; must complete as-is: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"stop_reason":"max_tokens"`) {
		t.Fatalf("length finish must map to max_tokens even with tool blocks:\n%s", out)
	}
	if strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("tool override must not fire on truncated input_json_delta:\n%s", out)
	}
}

// The complementary half: missing or contradictory ("stop") finish_reason with
// tool blocks still forces tool_use so subagent spawns are executed.
// (TestStreamStopReasonToolUseWhenToolEmitted covers the missing case; this
// pins the stop-with-tool-cases case explicitly.)
func TestStreamStopFinishWithToolCallsStillToolUse(t *testing.T) {
	sse := "data: " + `{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tu1","type":"function","function":{"name":"Bash","arguments":"{\"cmd\":\"ls\"}"}}]}}]}` + "\n\n" +
		"data: " + `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	rec := httptest.NewRecorder()
	if err := streamTranslate(rec, strings.NewReader(sse), "m"); err != nil {
		t.Fatal(err)
	}
	if out := rec.Body.String(); !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("stop+tool_calls must still relabel as tool_use:\n%s", out)
	}
}

// The non-stream sibling of fix #10: toAnthropic used to force tool_use even
// for finish_reason=length, pairing the truncated arguments (already degraded
// to {} by toolInput) with an executable-looking tool_use block.
func TestToAnthropicLengthWithToolCallsKeepsMaxTokens(t *testing.T) {
	raw := `{"id":"c1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"tu1","function":{"name":"Bash","arguments":"{\"cmd\": \"grep -r"}}]},"finish_reason":"length"}]}`
	var o openAIResponse
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	a := o.toAnthropic("m")
	if a.StopReason != "max_tokens" {
		t.Fatalf("StopReason = %q, want max_tokens (Claude Code must retry, not execute truncated input)", a.StopReason)
	}
	if len(a.Content) == 0 || a.Content[0].Type != "tool_use" {
		t.Fatalf("tool_use block missing entirely: %+v", a.Content)
	}
}

// Partial-but-useful streams still commit as-is (no regression from fix #1):
// content plus a known finish_reason reaching EOF without [DONE] closes
// normally, and a reasoning-only [DONE] stream is surfaced as text.
func TestStreamPartialContentWithFinishCommits(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := streamTranslate(rec, strings.NewReader(
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial thought\"}}]}\n\n"+
			"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
	), "m"); err != nil {
		t.Fatalf("content+finish at EOF must commit as-is: %v", err)
	}
	out := rec.Body.String()
	if !hasEvent(out, "message_stop") || !strings.Contains(out, `"end_turn"`) {
		t.Fatalf("commit path missing normal termination:\n%s", out)
	}
	if got := collectText(out); got != "partial thought" {
		t.Fatalf("text = %q, want the partial content preserved", got)
	}
}
