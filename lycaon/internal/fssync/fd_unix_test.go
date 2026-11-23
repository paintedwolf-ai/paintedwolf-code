//go:build darwin || linux

package fssync_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFDFlushesFilesAndDirectoriesWithSharedTestSupport(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "f"))
	testutil.FailErr(t, "create file", err)
	defer func() { _ = file.Close() }()
	_, err = file.WriteString("durable content")
	testutil.FailErr(t, "write file", err)
	handle, err := os.Open(dir)
	testutil.FailErr(t, "open directory", err)
	defer func() { _ = handle.Close() }()
	for _, target := range []*os.File{file, handle} {
		testutil.FailErr(t, "flush held descriptor", fssync.FD(int(target.Fd())))
	}
	if err := fssync.FD(-1); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("invalid descriptor flush = %v, want EBADF", err)
	}
}
