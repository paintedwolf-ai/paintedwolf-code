package bloblifecycle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestManagedRemovalWaitsForCaptureAndRejectsOutsidePaths(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "projects", "deleted")
	testutil.FailErr(t, "create managed tree", os.MkdirAll(target, 0o700))
	release := AcquirePublication(root)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- RemoveTree(root, target) }()
	<-started
	select {
	case err := <-done:
		release()
		t.Fatalf("cleanup passed active capture: %v", err)
	default:
	}
	_, err := os.Stat(target)
	testutil.FailErr(t, "captured directory remains", err)
	release()
	select {
	case err := <-done:
		testutil.FailErr(t, "finish deferred removal", err)
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not finish after capture")
	}
	if err := RemoveTree(root, root); err == nil {
		t.Fatal("cleanup accepted entire data root")
	}
	if err := RemoveTree(root, t.TempDir()); err == nil {
		t.Fatal("cleanup accepted outside path")
	}
}
