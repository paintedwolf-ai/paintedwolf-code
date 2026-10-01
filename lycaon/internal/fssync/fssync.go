// Package fssync centralizes durable write flushing to stable storage.
// Test binaries may relax flushing to avoid sync overhead.
package fssync

import (
	"os"
	"sync/atomic"
)

var relaxed atomic.Bool

// File flushes f to stable storage unless the process relaxed durability.
func File(f *os.File) error {
	if relaxed.Load() {
		return nil
	}
	return f.Sync()
}

// Relaxed reports whether flushes are skipped; platform code with its own
// flush primitive consults it.
func Relaxed() bool {
	return relaxed.Load()
}

// Relax skips every later flush. Only test support calls it.
func Relax() {
	relaxed.Store(true)
}
