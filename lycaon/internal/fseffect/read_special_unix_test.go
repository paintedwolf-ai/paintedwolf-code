//go:build !windows

package fseffect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestOpenReadDoesNotWaitForFIFOWriter(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create fifo", unix.Mkfifo(filepath.Join(root, "pipe"), 0o600))
	file, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: "pipe"})
	testutil.FailErr(t, "open fifo without writer", err)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	testutil.FailErr(t, "inspect opened descriptor", err)
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("mode=%v", info.Mode())
	}
}
