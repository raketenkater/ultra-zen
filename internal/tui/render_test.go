package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// goldens are the fixtures the whole Column contract hangs off: every item
// renders to exactly ONE physical line, tails right-align across rows, and
// the tier word survives width pressure when recent/ctx cannot.
func renderFixtureList(t *testing.T, delegate columnDelegate, width int) []string {
	t.Helper()
	items := []list.Item{
		groupHeaderItem{label: "Most used", count: 4},
		modelItem{m: models.Model{ID: "a", Name: "Alpha Model", Free: true, ContextLength: 204800}, recent: true},
		modelItem{m: models.Model{ID: "b", Name: "B", Free: false}},
		cycleItem{selected: 3},
		providerStatusItem{provider: "cerebras", kind: "keyless"},
	}
	lm := list.New(items, delegate, width, 10)
	configureList(&lm)
	var out []string
	for i, item := range items {
		var buf bytes.Buffer
		delegate.Render(&buf, lm, i, item)
		out = append(out, buf.String())
	}
	return out
}

// TestRenderOneLinePerItem is the pagination-math guarantee: bubbles divides
// available height by the uniform delegate Height(), so a row that renders
// two physical lines silently overflows the frame.
func TestRenderOneLinePerItem(t *testing.T) {
	for _, width := range []int{40, 80, 116} {
		for _, line := range renderFixtureList(t, columnDelegate{}, width) {
			if strings.Contains(line, "\n") {
				t.Fatalf("width %d: rendered %d lines: %q", width, strings.Count(line, "\n")+1, line)
			}
		}
	}
}

// TestRenderFitsWidth pins that no rendered row exceeds the list width at
// narrow terminals (padding math and drop order must converge).
func TestRenderFitsWidth(t *testing.T) {
	for _, width := range []int{40, 60, 80, 116} {
		for _, line := range renderFixtureList(t, columnDelegate{}, width) {
			if w := ansi.StringWidth(ansi.Strip(line)); w > width {
				t.Fatalf("width %d: row is %d wide: %q", width, w, ansi.Strip(line))
			}
		}
	}
}

// TestRenderTailRightEdgeAligns pins the column contract: every row with a
// tail (models, cycle, status) lands its tail's last character on the same
// column; section headers have no tail.
func TestRenderTailRightEdgeAligns(t *testing.T) {
	width := 80
	lines := renderFixtureList(t, columnDelegate{}, width)
	checked := 0
	for i, line := range lines {
		if i == 0 {
			continue // header: no tail
		}
		plain := strings.TrimRight(ansi.Strip(line), " ")
		if plain == "" {
			continue
		}
		if got := ansi.StringWidth(plain); got != width {
			t.Fatalf("row %d right edge = %d, want %d: %q", i, got, width, plain)
		}
		checked++
	}
	if checked < 3 {
		t.Fatalf("expected at least 3 tail rows, got %d", checked)
	}
}

// TestRenderTierWordSurvivesWidthPressure pins the drop order: under enough
// width pressure the row sheds "recent", then the ctx word — never the tier
// word (parts[0]), which is the row's identity.
func TestRenderTierWordSurvivesWidthPressure(t *testing.T) {
	delegate := columnDelegate{}
	long := modelItem{
		m:      models.Model{ID: "x", Name: "An Extremely Long Model Display Name That Cannot Fit", Free: true, ContextLength: 131072},
		recent: true,
	}
	for _, width := range []int{20, 28, 40} {
		lm := list.New([]list.Item{long}, delegate, width, 10)
		configureList(&lm)
		var buf bytes.Buffer
		delegate.Render(&buf, lm, 0, long)
		plain := ansi.Strip(buf.String())
		if !strings.Contains(plain, "free") {
			t.Fatalf("width %d: tier word dropped under pressure: %q", width, plain)
		}
	}
}

// TestRenderPoolGutter keeps names at col 4 on the pool screen and marks
// membership in the gutter, never in the name.
func TestRenderPoolGutter(t *testing.T) {
	delegate := columnDelegate{showMark: true}
	rows := []fallbackRow{
		{provider: "groq", modelID: "llama", kind: rowModel, inPool: true, free: true},
		{provider: "groq", modelID: "qwen", kind: rowModel, inPool: false, free: true},
		{provider: "cerebras", kind: rowNoKey},
	}
	lm := list.New([]list.Item{rows[0], rows[1], rows[2]}, delegate, 80, 10)
	configureList(&lm)
	for i, row := range rows {
		var buf bytes.Buffer
		delegate.Render(&buf, lm, i, row)
		plain := ansi.Strip(buf.String())
		if len(plain) < 4 {
			t.Fatalf("row %d too short: %q", i, plain)
		}
		if i < 2 {
			wantMark := gMarkOn
			if !row.inPool {
				wantMark = gMarkOff
			}
			runes := []rune(plain)
			if string(runes[2]) != wantMark {
				t.Fatalf("row %d mark slot = %q, want %q in %q", i, string(runes[2]), wantMark, plain)
			}
			if !strings.HasPrefix(string(runes[4:]), row.modelID) {
				t.Fatalf("row %d name not at col 4: %q", i, plain)
			}
		}
	}
}
