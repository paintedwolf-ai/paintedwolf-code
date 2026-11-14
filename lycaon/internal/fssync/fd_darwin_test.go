//go:build darwin

package fssync

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFlushToStableStorageFallsBackOnlyWhenUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name          string
		full          []error
		want          error
		fullCalls     int
		fallbackCalls int
	}{
		{name: "supported", full: []error{nil}, fullCalls: 1},
		{name: "interrupted", full: []error{syscall.EINTR, nil}, fullCalls: 2},
		{name: "unsupported", full: []error{syscall.ENOTSUP}, fullCalls: 1, fallbackCalls: 1},
		{name: "failure", full: []error{syscall.EIO}, want: syscall.EIO, fullCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fullCalls, fallbackCalls := 0, 0
			err := flushToStableStorage(7, func(int) error {
				fullCalls++
				return tc.full[fullCalls-1]
			}, func(int) error {
				fallbackCalls++
				return nil
			})
			if !errors.Is(err, tc.want) || fullCalls != tc.fullCalls || fallbackCalls != tc.fallbackCalls {
				t.Fatalf("err=%v full=%d fallback=%d", err, fullCalls, fallbackCalls)
			}
		})
	}
}

func TestFDFlushesFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal("create file: ", err)
	}
	defer func() { _ = file.Close() }()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal("open directory: ", err)
	}
	defer func() { _ = handle.Close() }()
	for _, target := range []*os.File{file, handle} {
		if err := FD(int(target.Fd())); err != nil {
			t.Fatalf("flush %s: %v", target.Name(), err)
		}
	}
}

func TestFileAndFDShareTheFlushPath(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal("create file: ", err)
	}
	fd := int(file.Fd())
	if err := flush(file); err != nil {
		t.Fatalf("flush open file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal("close file: ", err)
	}
	if err := flushFD(fd); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("flushFD on a closed descriptor = %v", err)
	}
	var pathErr *os.PathError
	if err := flush(file); !errors.As(err, &pathErr) {
		t.Fatalf("flush on a closed file = %v, want a path error", err)
	}
}
