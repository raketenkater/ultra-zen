package tui

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// TestMain pins the renderer to the ANSI-16 profile. lipgloss strips every
// style to plain text when stdout is not a TTY (the test runner), but the
// Column design carries real information in color — the free tier's accent
// tail word versus muted paid — so the goldens below assert the actual SGR
// sequences. The profile is set on the global renderer before any style
// renders, and t.Setenv cannot do this job: termenv reads the environment
// once at package init. Assertions compare against style.Render values
// rather than hardcoded sequences, so the dark/light AdaptiveColor branch the
// renderer happens to pick stays irrelevant.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.ANSI)
	os.Exit(m.Run())
}
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
// membership in the gutter, never in the name. Pool members show their
// rotation rank (1-based pos) for ranks 1..9; beyond that the membership
// glyph stands in, since two digits would break the 4-column gutter.
func TestRenderPoolGutter(t *testing.T) {
	delegate := columnDelegate{showMark: true}
	rows := []fallbackRow{
		{provider: "groq", modelID: "llama", kind: rowModel, inPool: true, pos: 1, free: true},
		{provider: "groq", modelID: "qwen", kind: rowModel, inPool: true, pos: 12, free: true},
		{provider: "groq", modelID: "gemma", kind: rowModel, inPool: false, free: true},
		{provider: "cerebras", kind: rowNoKey},
	}
	lm := list.New([]list.Item{rows[0], rows[1], rows[2], rows[3]}, delegate, 80, 10)
	configureList(&lm)
	for i, row := range rows {
		var buf bytes.Buffer
		delegate.Render(&buf, lm, i, row)
		plain := ansi.Strip(buf.String())
		if len(plain) < 4 {
			t.Fatalf("row %d too short: %q", i, plain)
		}
		if i < 3 {
			wantMark := gMarkOff
			switch {
			case row.inPool && row.pos >= 1 && row.pos <= 9:
				wantMark = strconv.Itoa(row.pos)
			case row.inPool:
				wantMark = gMarkOn
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

// TestRenderFreeTailPops pins the free-tier highlight: on every width the
// tier word of a free row carries the accent style (the one color besides the
// cursor), while a paid row's whole tail — and a free row's ctx/recency
// parts — stay muted. This is what makes free vs paid scannable at a glance.
func TestRenderFreeTailPops(t *testing.T) {
	wantFree := accentStyle.Render("free")
	wantPaid := mutedStyle.Render("paid")
	if wantFree == wantPaid {
		t.Fatal("fixture degraded: accent and muted render identically (color profile not forced?)")
	}
	for _, width := range []int{40, 80, 116} {
		lines := renderFixtureList(t, columnDelegate{}, width)
		free, paid := lines[1], lines[2]
		if !strings.Contains(free, wantFree) {
			t.Fatalf("width %d: free row tail not accent-styled: %q", width, free)
		}
		if !strings.Contains(paid, wantPaid) || strings.Contains(paid, wantFree) {
			t.Fatalf("width %d: paid row tail wrongly highlighted: %q", width, paid)
		}
		// Only the tier word pops — the ctx word beside it stays muted.
		if !strings.Contains(free, mutedStyle.Render("200k")) {
			t.Fatalf("width %d: free row ctx should stay muted: %q", width, free)
		}
	}
}

// TestRenderTierWordSurvivesInFreeTail pins the documented drop order on the
// free-highlighted tail: at width 40 the styled "free" survives while recent
// and ctx are shed, and the row still fits the column budget.
func TestRenderTierWordSurvivesInFreeTail(t *testing.T) {
	delegate := columnDelegate{}
	long := modelItem{
		m:      models.Model{ID: "x", Name: "An Extremely Long Model Display Name That Cannot Fit", Free: true, ContextLength: 131072},
		recent: true,
	}
	for _, width := range []int{40, 60} {
		lm := list.New([]list.Item{long}, delegate, width, 10)
		configureList(&lm)
		var buf bytes.Buffer
		// index -1 keeps the row unselected: only the unselected branch
		// styles the tier word individually (the cursor line is all bold).
		delegate.Render(&buf, lm, -1, long)
		if !strings.Contains(buf.String(), accentStyle.Render("free")) {
			t.Fatalf("width %d: styled tier word dropped: %q", width, buf.String())
		}
		if w := ansi.StringWidth(buf.String()); w > width {
			t.Fatalf("width %d: row is %d wide: %q", width, w, ansi.Strip(buf.String()))
		}
	}
}
