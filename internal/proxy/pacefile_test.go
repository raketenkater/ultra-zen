package proxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAccountPaceFileSharesSlotsAcrossCalls verifies that two pacing calls for
// the same API key reserve disjoint, advancing slots (i.e. the aggregate rate is
// 1/interval even across "processes" simulated by separate calls).
func TestAccountPaceFileSharesSlotsAcrossCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")

	const interval = 50 * time.Millisecond
	start := time.Now()

	// First call reserves slot ~now.
	if err := waitAccountOpenRouterSlot(context.Background(), interval, "shared-key"); err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)

	// Second call must reserve a later slot (the committed one advanced by
	// interval), so it sleeps longer than a fresh-slot call would.
	if err := waitAccountOpenRouterSlot(context.Background(), interval, "shared-key"); err != nil {
		t.Fatal(err)
	}
	second := time.Since(start)

	if second-first < interval/2 {
		t.Fatalf("second call did not advance past the first: first=%v second=%v (interval=%v)", first, second, interval)
	}
}

// TestPaceFileKeyedByAccount verifies distinct API keys get distinct pace files.
func TestPaceFileKeyedByAccount(t *testing.T) {
	p1 := paceFilePath("key-A")
	p2 := paceFilePath("key-B")
	if p1 == p2 {
		t.Fatalf("distinct keys share a pace file: %s", p1)
	}
	// Same key -> same file.
	if p3 := paceFilePath("key-A"); p3 != p1 {
		t.Fatalf("same key produced different files: %s vs %s", p3, p1)
	}
}

// TestPaceFileCommitRoundtrip verifies the committed slot round-trips to disk.
func TestPaceFileCommitRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pace")
	if err := commitPaceFile(path, 123456789); err != nil {
		t.Fatal(err)
	}
	got, err := readPaceFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != 123456789 {
		t.Fatalf("read = %d, want 123456789", got)
	}
}

// TestWaitAccountSlotBestEffortNoPanic ensures a missing HOME/cache dir doesn't
// panic; it best-effort sleeps the local slot.
func TestWaitAccountSlotBestEffortNoPanic(t *testing.T) {
	t.Setenv("HOME", filepath.Join(os.TempDir(), "no-such-ultra-zen-home-"+time.Now().String()))
	t.Setenv("XDG_RUNTIME_DIR", "")
	if err := waitAccountOpenRouterSlot(context.Background(), time.Millisecond, "k"); err != nil {
		t.Fatal(err)
	}
}
