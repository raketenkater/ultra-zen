// Package flock provides an advisory inter-process lock for the shared JSON
// stores (unavailable-models.json, free-pool.json) that multiple concurrent
// ultra-zen sessions read-modify-write.
package flock

import (
	"os"
	"path/filepath"
	"syscall"
)

// Guard is an advisory inter-process lock around a shared config file.
// Without a cross-process lock two sessions can both read the old file and each
// rename over the other, losing one session's update. flock serializes that
// read-modify-write cycle across processes. Within one process the caller's own
// mutex still applies.
type Guard struct {
	f *os.File
}

// Lock takes an exclusive advisory lock on path+".lock" and returns the guard.
// Caller must call Close when done. It is best-effort: an error means locking is
// unavailable, and the caller should proceed without it (the rename is still
// atomic, so the only risk is a lost update across processes).
func Lock(path string) *Guard {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return &Guard{}
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return &Guard{}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return &Guard{}
	}
	return &Guard{f: f}
}

// Close releases the flock and closes the lock file.
func (g *Guard) Close() {
	if g == nil || g.f == nil {
		return
	}
	_ = syscall.Flock(int(g.f.Fd()), syscall.LOCK_UN)
	_ = g.f.Close()
	g.f = nil
}