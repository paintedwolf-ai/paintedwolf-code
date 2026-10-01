//go:build darwin || linux

package fileclone

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClonePreservesSnapshotAcrossSourceWritesAndRemoval(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "live"), filepath.Join(root, "snapshot")
	original := bytes.Repeat([]byte("retained history\n"), 1<<16)
	testutil.FailErr(t, "seed source", os.WriteFile(source, original, 0o600))
	started := time.Now()
	cloned, err := Clone(source, destination)
	testutil.FailErr(t, "clone source", err)
	t.Logf("kernel copy-on-write clone: supported=%t bytes=%d duration=%s", cloned, len(original), time.Since(started))
	if !cloned {
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatalf("unsupported clone left a destination: %v", err)
		}
		return
	}
	file, err := os.OpenFile(source, os.O_WRONLY, 0)
	testutil.FailErr(t, "open live source", err)
	_, err = file.WriteAt([]byte("changed in place"), 0)
	testutil.FailErr(t, "change live bytes", err)
	testutil.FailErr(t, "close live source", file.Close())
	testutil.FailErr(t, "unlink live source", os.Remove(source))
	got, err := os.ReadFile(destination)
	testutil.FailErr(t, "read retained clone", err)
	if !bytes.Equal(got, original) {
		t.Fatal("snapshot changed with the live file")
	}
}

func TestCloneNeverReplacesAnExistingDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	testutil.FailErr(t, "seed source", os.WriteFile(source, []byte("new bytes"), 0o600))
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			destination := filepath.Join(root, kind)
			switch kind {
			case "file":
				testutil.FailErr(t, "seed destination", os.WriteFile(destination, []byte("previous"), 0o600))
			case "directory":
				testutil.FailErr(t, "seed directory", os.Mkdir(destination, 0o700))
			case "symlink":
				testutil.FailErr(t, "seed symlink", os.Symlink(source, destination))
			}
			before, err := os.Lstat(destination)
			testutil.FailErr(t, "stat previous", err)
			cloned, err := Clone(source, destination)
			if cloned || err == nil {
				t.Fatalf("existing destination was accepted: cloned=%t err=%v", cloned, err)
			}
			after, err := os.Lstat(destination)
			testutil.FailErr(t, "preserve destination", err)
			if !os.SameFile(before, after) {
				t.Fatal("existing destination replaced")
			}
		})
	}
}

func TestCloneRefusesSymlinkSources(t *testing.T) {
	root := t.TempDir()
	target, source, destination := filepath.Join(root, "target"), filepath.Join(root, "source"), filepath.Join(root, "snapshot")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("outside bytes"), 0o600))
	testutil.FailErr(t, "seed source symlink", os.Symlink(target, source))
	if cloned, err := Clone(source, destination); cloned || err == nil {
		t.Fatalf("symlink source accepted: cloned=%t err=%v", cloned, err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("refused clone left destination: %v", err)
	}
}
