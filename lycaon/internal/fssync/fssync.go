// Package fssync centralizes durable write flushing to stable storage.
package fssync

import "os"

// File flushes f to stable storage.
func File(f *os.File) error {
	return flush(f)
}
