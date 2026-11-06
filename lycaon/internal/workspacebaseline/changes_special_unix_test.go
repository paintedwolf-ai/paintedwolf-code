//go:build !windows

package workspacebaseline_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"golang.org/x/sys/unix"
)

// A file with no writer must not stall a comparison: a blocking open would wait
// for one forever. The cleanup releases a stalled open so a failure stays a failure.
func TestComparePathOfASpecialFileHasNoText(t *testing.T) {
	branch, reader := sealedBranch(t, map[string][]byte{"keep.txt": []byte("keep\n")})
	pipe := filepath.Join(branch, "pipe")
	testutil.FailErr(t, "mkfifo", unix.Mkfifo(pipe, 0o600))
	t.Cleanup(func() {
		if fd, err := unix.Open(pipe, unix.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			_ = unix.Close(fd)
		}
	})
	// A relative name keeps the socket path within the platform's length limit.
	t.Chdir(branch)
	listener, err := net.Listen("unix", "sock")
	testutil.FailErr(t, "listen", err)
	defer func() { _ = listener.Close() }()

	for _, path := range []string{"pipe", "sock"} {
		done := make(chan workspacebaseline.PathComparison, 1)
		go func() {
			got, err := reader.ComparePath(t.Context(), nil, branch, path)
			if err != nil {
				t.Errorf("compare %s: %v", path, err)
			}
			done <- got
		}()
		select {
		case got := <-done:
			if got.Kind != workspacebaseline.ChangeCreated || got.HasAfter || got.After != "" {
				t.Fatalf("%s = %+v, want a change with no text", path, got)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("comparing %s did not return", path)
		}
	}
	if _, err := os.Lstat(pipe); err != nil {
		t.Fatalf("comparison disturbed the fifo: %v", err)
	}
}

// The tree walk lists a special file without opening it.
func TestChangeSetListsASpecialFile(t *testing.T) {
	branch, reader := sealedBranch(t, map[string][]byte{"keep.txt": []byte("keep\n")})
	testutil.FailErr(t, "mkfifo", unix.Mkfifo(filepath.Join(branch, "pipe"), 0o600))
	changes, err := reader.ChangeSet(t.Context(), nil, branch)
	testutil.FailErr(t, "change set", err)
	if len(changes) != 1 || changes[0].Path != "pipe" || changes[0].Kind != workspacebaseline.ChangeCreated {
		t.Fatalf("changes = %+v, want the fifo as created", changes)
	}
}
