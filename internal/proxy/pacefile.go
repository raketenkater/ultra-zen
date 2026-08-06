package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/raketenkater/ultra-zen/internal/flock"
)

// paceFilePath returns the on-disk file that holds the account-shared next
// OpenRouter slot. Multiple concurrent ultra-zen processes using the same API
// key share this file (via flock) so the aggregate request rate respects the
// account's real RPM instead of each process pacing independently at NxRPM.
// The path is keyed by a hash of the API key so different accounts never
// contend on the same limiter.
func paceFilePath(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	tag := hex.EncodeToString(sum[:6])
	base := os.Getenv("XDG_RUNTIME_DIR")
	dir := filepath.Join(os.Getenv("HOME"), ".cache", "ultra-zen")
	if base != "" {
		dir = filepath.Join(base, "ultra-zen")
	}
	return filepath.Join(dir, "openrouter-pace-"+tag)
}

// paceFile is the account-shared token bucket record: the next slot timestamp
// (UnixNano) until which the shared limiter is committed.
type paceFile struct {
	nextSlotUnixNano int64
}

// readPaceFile loads the committed next slot under the flock, then returns it.
func readPaceFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// commitPaceFile atomically records the next slot under the flock.
func commitPaceFile(path string, nextSlot int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(nextSlot, 10)), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// waitAccountOpenRouterSlot is the account-shared pacing gate. It reads the
// committed next slot under a cross-process flock, advances it by interval, and
// sleeps until the reserved slot. Because the lock file is shared per API key,
// the aggregate pace across all concurrent ultra-zen sessions respects the
// account's RPM — a single session's limiter no longer multiplies.
func waitAccountOpenRouterSlot(ctx context.Context, interval time.Duration, apiKey string) error {
	if interval <= 0 || apiKey == "" {
		return nil
	}
	path := paceFilePath(apiKey)
	now := time.Now()

	// Reserve the next slot atomically across processes.
	guard := flock.Lock(path + ".lock")
	defer guard.Close()

	// Start from the later of "now" and the last committed slot, then book one
	// interval past it. Under the per-key flock every concurrent session takes
	// a disjoint slot, so the aggregate rate is 1/interval regardless of how
	// many sessions share the account.
	nextSlot := now
	if existing, err := readPaceFile(path); err == nil && existing > now.UnixNano() {
		nextSlot = time.Unix(0, existing)
	}
	slot := nextSlot
	committed := nextSlot.Add(interval)
	// Best-effort persistence: if the file can't be written the session still
	// sleeps for this reserved slot (the in-process gateMu serializes its own
	// requests), just without cross-process sharing.
	_ = commitPaceFile(path, committed.UnixNano())

	return sleepContext(ctx, time.Until(slot))
}
