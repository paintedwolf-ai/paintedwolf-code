package sourcesnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCaptureLeavesIOAvailableOutsideFileReads(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "one.go", "package one\n")
	writeSource(t, root, "two.go", "package two\n")
	var admissionErr error
	store.onSurvey = func(string) {
		release, err := store.broker.Acquire(t.Context(), backgroundwork.Request{
			Priority:  backgroundwork.PriorityInteractive,
			Resources: []backgroundwork.Resource{backgroundwork.ResourceIO},
			MaxWait:   time.Second,
		})
		admissionErr = err
		if err == nil {
			release()
		}
	}
	snapshot, err := store.EnsurePath(t.Context(), root, VerifyContent)
	testutil.FailErr(t, "capture complete repository", err)
	testutil.FailErr(t, "admit interactive I/O during repository survey", admissionErr)
	if snapshot.FileCount != 2 {
		t.Fatalf("captured %d files, want 2", snapshot.FileCount)
	}
}

func TestReleaseDoesNotWaitForAnotherRootsCapture(t *testing.T) {
	store := openSnapshotStore(t)
	kept, gone := t.TempDir(), t.TempDir()
	writeSource(t, gone, "old.go", "package old\n")
	_, err := store.EnsurePath(t.Context(), gone, VerifyContent)
	testutil.FailErr(t, "capture detached root", err)
	writeSource(t, kept, "new.go", "package new\n")
	store.onCapture = func(string) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		testutil.FailErr(t, "release unrelated root during capture", store.ReleaseRoots(ctx, []Root{{Path: gone}}))
	}
	snapshot, err := store.EnsurePath(t.Context(), kept, VerifyContent)
	testutil.FailErr(t, "complete retained root capture", err)
	if snapshot.FileCount != 1 {
		t.Fatalf("retained root captured %d files, want 1", snapshot.FileCount)
	}
	head, err := store.HeadIDForPath(t.Context(), gone)
	testutil.FailErr(t, "read released root head", err)
	if head != "" {
		t.Fatalf("released root still has head %s", head)
	}
}

func TestReleaseWaitsOnlyForItsRootsAndCancellationPreservesThem(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "one.go", "package one\n")
	snapshot, err := store.EnsurePath(t.Context(), root, VerifyContent)
	testutil.FailErr(t, "capture root", err)
	release, err := store.acquireRootStorage(t.Context(), []Root{{Path: root}}, false)
	testutil.FailErr(t, "retain root reader", err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err = store.ReleaseRoots(ctx, []Root{{Path: root}})
	release()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("release crossed active root reader: %v", err)
	}
	head, err := store.HeadIDForPath(t.Context(), root)
	testutil.FailErr(t, "read retained head", err)
	if head != snapshot.ID {
		t.Fatalf("canceled release changed head: got %s want %s", head, snapshot.ID)
	}
	if len(store.rootStorage) != 0 {
		t.Fatalf("root admission retained %d unused gates", len(store.rootStorage))
	}
}
