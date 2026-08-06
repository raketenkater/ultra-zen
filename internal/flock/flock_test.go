package flock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLockSerializedAcrossLockers verifies that a second Lock on the same path
// blocks while the first is held (cross-process / cross-call mutual exclusion),
// and releases once the first guard closes.
func TestLockSerializesAcrossLockCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	g1 := Lock(path)
	if g1 == nil || g1.f == nil {
		t.Fatal("first Lock returned no held guard")
	}

	// A second Lock on the same lock file must block until g1 releases.
	acquired := make(chan struct{})
	go func() {
		g2 := Lock(path)
		close(acquired)
		g2.Close()
	}()

	select {
	case <-acquired:
		t.Fatal("second Lock acquired while first was still held")
	case <-time.After(150 * time.Millisecond):
		// Correct: blocked.
	}

	g1.Close()

	select {
	case <-acquired:
		// Released; second lock acquired.
	case <-time.After(2 * time.Second):
		t.Fatal("second Lock did not acquire after first released")
	}
}

// TestLockWithoutErrorOnMissingDir ensures best-effort behavior does not panic
// when the config directory does not exist.
func TestLockBestEffortOnMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no", "such", "dir", "state.json")
	g := Lock(path)
	// Best-effort: either nil f (unavailable) or an error path; must not panic.
	g.Close()
}

func TestLockFileCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	g := Lock(path)
	g.Close()
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
}