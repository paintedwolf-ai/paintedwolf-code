package layeredfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleaseViewWaitsForAcquiredCheckpointBarrierAndCompletesOnEndBarrier(t *testing.T) {
	rootDir := t.TempDir()
	backingDir := t.TempDir()
	testutil.FailErr(t, "write lower barrier.txt", os.WriteFile(filepath.Join(rootDir, "barrier.txt"), []byte("v1\n"), 0o644))

	drainingEntered := make(chan struct{})
	var drainingOnce sync.Once

	c, err := New(Config{
		ViewID:     "view-r5-barrier-release",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			ReleaseWaitHook: func(stage string) {
				if stage == "draining" {
					drainingOnce.Do(func() { close(drainingEntered) })
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)

	qp := QualifiedPath{RootID: "r0", Rel: "barrier.txt"}
	_, err = c.WriteFile(qp, []byte("v2\n"), 0o644)
	testutil.FailErr(t, "WriteFile barrier.txt", err)

	token, err := c.BeginCheckpointBarrier(context.Background())
	testutil.FailErr(t, "BeginCheckpointBarrier", err)
	var endBarrierOnce sync.Once
	endBarrier := func() error {
		var bErr error
		endBarrierOnce.Do(func() {
			bErr = c.EndCheckpointBarrier(token)
		})
		return bErr
	}
	t.Cleanup(func() {
		_ = endBarrier()
		_ = c.ReleaseView()
	})

	releaseDone := make(chan error, 1)
	go func() {
		releaseDone <- c.ReleaseView()
	}()

	select {
	case <-drainingEntered:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for ReleaseView to enter drain")
	}

	if _, wErr := c.WriteFile(qp, []byte("v3\n"), 0o644); !errors.Is(wErr, ErrViewReleased) {
		t.Fatalf("expected ErrViewReleased while ReleaseView is draining, got %v", wErr)
	}

	select {
	case rErr := <-releaseDone:
		t.Fatalf("ReleaseView completed before EndCheckpointBarrier was called: %v", rErr)
	default:
	}

	// EndCheckpointBarrier with the acquired token must succeed and unblock ReleaseView.
	testutil.FailErr(t, "EndCheckpointBarrier during ReleaseView drain", endBarrier())

	select {
	case rErr := <-releaseDone:
		testutil.FailErr(t, "ReleaseView after EndCheckpointBarrier", rErr)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for ReleaseView to complete after EndCheckpointBarrier")
	}

	snap, err := c.LoadPersistedJournal()
	testutil.FailErr(t, "LoadPersistedJournal", err)
	if len(snap.Changes) != 1 || snap.Changes[0].Path != qp {
		t.Fatalf("expected 1 persisted change for barrier.txt, got %+v", snap.Changes)
	}
}

func TestReleaseViewRetryOvertakingWaiterPreservesAttemptError(t *testing.T) {
	rootDir := t.TempDir()
	backingDir := t.TempDir()
	testutil.FailErr(t, "write lower overtake.txt", os.WriteFile(filepath.Join(rootDir, "overtake.txt"), []byte("v1\n"), 0o644))

	var failSync atomic.Bool
	var pauseSync atomic.Bool
	syncEntered := make(chan struct{})
	unblockSync := make(chan struct{})
	var syncOnce sync.Once
	var unblockSyncOnce sync.Once

	waiterJoined := make(chan struct{})
	waiterAwoke := make(chan struct{})
	allowWaiterReturn := make(chan struct{})
	var joinedOnce sync.Once
	var waiterOnce sync.Once
	var allowWaiterOnce sync.Once

	c, err := New(Config{
		ViewID:     "view-r5-overtake",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			JournalSyncHook: func(phase string) error {
				if phase == "journal_dir_sync" {
					if pauseSync.Load() {
						syncOnce.Do(func() { close(syncEntered) })
						<-unblockSync
					}
					if failSync.Load() {
						return syscall.EIO
					}
				}
				return nil
			},
			ReleaseWaitHook: func(stage string) {
				switch stage {
				case "joined":
					joinedOnce.Do(func() { close(waiterJoined) })
				case "done":
					waiterOnce.Do(func() {
						close(waiterAwoke)
						<-allowWaiterReturn
					})
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() {
		failSync.Store(false)
		pauseSync.Store(false)
		unblockSyncOnce.Do(func() { close(unblockSync) })
		allowWaiterOnce.Do(func() { close(allowWaiterReturn) })
		_ = c.ReleaseView()
	})

	qp := QualifiedPath{RootID: "r0", Rel: "overtake.txt"}
	_, err = c.WriteFile(qp, []byte("v2\n"), 0o644)
	testutil.FailErr(t, "WriteFile overtake.txt", err)

	failSync.Store(true)
	pauseSync.Store(true)

	attempt1LeaderErr := make(chan error, 1)
	go func() {
		attempt1LeaderErr <- c.ReleaseView()
	}()

	select {
	case <-syncEntered:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for attempt 1 journal_dir_sync")
	}

	attempt1WaiterErr := make(chan error, 1)
	go func() {
		attempt1WaiterErr <- c.ReleaseView()
	}()

	select {
	case <-waiterJoined:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for attempt 1 waiter to join active release attempt")
	}

	pauseSync.Store(false)
	unblockSyncOnce.Do(func() { close(unblockSync) })

	if err := <-attempt1LeaderErr; !errors.Is(err, syscall.EIO) {
		t.Fatalf("attempt 1 leader expected EIO, got %v", err)
	}
	select {
	case <-waiterAwoke:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for attempt 1 joined waiter to wake")
	}

	// While attempt 1's joined waiter is paused before reading its result, run retry B to completion.
	failSync.Store(false)
	testutil.FailErr(t, "retry B ReleaseView succeeds while waiter A is paused", c.ReleaseView())

	allowWaiterOnce.Do(func() { close(allowWaiterReturn) })
	if err := <-attempt1WaiterErr; !errors.Is(err, syscall.EIO) {
		t.Fatalf("attempt 1 joined waiter expected attempt 1 EIO despite retry B overtaking, got %v", err)
	}
}

func TestSyncHandleCoversReadOnlyDescriptorsOfPrivateObjects(t *testing.T) {
	rootDir, backingDir := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write lower.txt", os.WriteFile(filepath.Join(rootDir, "lower.txt"), []byte("base\n"), 0o644))

	var syncCalls atomic.Int32
	var failSync atomic.Bool
	c, err := New(Config{
		ViewID:     "view-sync-ro",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			JournalSyncHook: func(op string) error {
				if op == "handle_sync" {
					syncCalls.Add(1)
					if failSync.Load() {
						return syscall.EIO
					}
				}
				return nil
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { _ = c.ReleaseView() })

	lower, _, err := c.Open(QualifiedPath{RootID: "r0", Rel: "lower.txt"}, OpenReadOnly, 0)
	testutil.FailErr(t, "open lower", err)
	testutil.FailErr(t, "sync lower descriptor", c.SyncHandle(lower))
	if syncCalls.Load() != 0 {
		t.Fatalf("a descriptor on an untouched lower object has nothing to sync, got %d syncs", syncCalls.Load())
	}
	testutil.FailErr(t, "close lower", c.CloseHandle(lower))

	qp := QualifiedPath{RootID: "r0", Rel: "private.txt"}
	_, err = c.WriteFile(qp, []byte("x"), 0o444)
	testutil.FailErr(t, "write private", err)
	ro, _, err := c.Open(qp, OpenReadOnly, 0)
	testutil.FailErr(t, "open private read-only", err)
	testutil.FailErr(t, "sync read-only descriptor", c.SyncHandle(ro))
	if syncCalls.Load() != 1 {
		t.Fatalf("a read-only descriptor on private content must sync it, got %d syncs", syncCalls.Load())
	}
	failSync.Store(true)
	if sErr := c.SyncHandle(ro); !errors.Is(sErr, syscall.EIO) {
		t.Fatalf("expected EIO to propagate through a read-only descriptor, got %v", sErr)
	}
	failSync.Store(false)
	testutil.FailErr(t, "close private", c.CloseHandle(ro))
}
