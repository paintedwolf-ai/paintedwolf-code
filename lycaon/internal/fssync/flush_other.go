//go:build !darwin

package fssync

import "os"

func flush(f *os.File) error { return f.Sync() }
