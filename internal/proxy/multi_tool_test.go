package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Decode events to check block indices structurally.
func collectBlocks(out string) []map[string]any {
	var blocks []map[string]any
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if ev["type"] == "content_block_start" {
			blocks = append(blocks, ev)
		}
	}
	return blocks
}

// Two tool calls announced together (indices 0 and 1) in one turn.
func TestStreamParallelToolCallsIndices(t *testing.T) {
	sse := "data: " + `{"id":"c1","choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
		"data: " + `{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tu0","type":"function","function":{"name":"spawn_agent","arguments":"{}"}},{"index":1,"id":"tu1","type":"function","function":{"name":"spawn_agent","arguments":"{}"}}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	if err := streamTranslate(rec, strings.NewReader(sse), "m"); err != nil {
		t.Fatal(err)
	}
	blocks := collectBlocks(rec.Body.String())
	if len(blocks) != 2 {
		t.Fatalf("expected 2 tool blocks, got %d", len(blocks))
	}
	idx0 := int(blocks[0]["index"].(float64))
	idx1 := int(blocks[1]["index"].(float64))
	t.Logf("block0 index=%d id=%v, block1 index=%d id=%v", idx0, blocks[0]["content_block"].(map[string]any)["id"], idx1, blocks[1]["content_block"].(map[string]any)["id"])
	if idx0 == idx1 {
		t.Errorf("COLLISION: both tool_use blocks got index %d (Anthropic requires unique sequential indices)", idx0)
	}
}

// Second tool call announced in a separate later chunk reusing index 0.
func TestStreamSecondToolInLaterChunk(t *testing.T) {
	sse := "data: " + `{"id":"c1","choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
		"data: " + `{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tu0","type":"function","function":{"name":"spawn_agent","arguments":"{\"a\":1}"}}]}}]}` + "\n\n" +
		"data: " + `{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":1,"id":"tu1","type":"function","function":{"name":"spawn_agent","arguments":"{\"b\":2}"}}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	if err := streamTranslate(rec, strings.NewReader(sse), "m"); err != nil {
		t.Fatal(err)
	}
	out := rec.Body.String()
	blocks := collectBlocks(out)
	t.Logf("blocks: %d", len(blocks))
	for i, b := range blocks {
		cb := b["content_block"].(map[string]any)
		t.Logf("  block%d index=%d id=%v", i, int(b["index"].(float64)), cb["id"])
	}
	if len(blocks) != 2 {
		t.Errorf("expected 2 distinct tool_use blocks, got %d — second tool call's id likely dropped", len(blocks))
	}
	if !strings.Contains(out, `"id":"tu1"`) {
		t.Errorf("second tool id tu1 never emitted as its own block")
	}
}
