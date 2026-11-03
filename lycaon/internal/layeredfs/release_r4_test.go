package layeredfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleasePropagatesHashReadAndWriteFailuresAndRecoversOnRetry(t *testing.T) {
	t.Run("hash_read_eio_and_concurrent_callers", func(t *testing.T) {
		rootDir := t.TempDir()
		backingDir := t.TempDir()
		testutil.FailErr(t, "write lower hello.txt", os.WriteFile(filepath.Join(rootDir, "hello.txt"), []byte("base-v1\n"), 0o644))

		var injectHashEIO atomic.Bool
		var blockFirstAttempt atomic.Bool
		hookEntered := make(chan struct{})
		unblockHook := make(chan struct{})
		waiterJoined := make(chan struct{})
		var enterOnce sync.Once
		var unblockOnce sync.Once
		var joinedOnce sync.Once

		c, err := New(Config{
			ViewID:     "view-r4-hash-eio",
			BackingDir: backingDir,
			Roots: []RootSpec{
				{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true},
			},
			FaultHooks: &FaultHooks{
				WorkingFileHashReader: func(qpath QualifiedPath, r io.Reader) (io.Reader, error) {
					if qpath.Rel == "hello.txt" {
						if blockFirstAttempt.Load() {
							enterOnce.Do(func() { close(hookEntered) })
							<-unblockHook
						}
						if injectHashEIO.Load() {
							return nil, syscall.EIO
						}
					}
					return r, nil
				},
				ReleaseWaitHook: func(stage string) {
					if stage == "joined" {
						joinedOnce.Do(func() { close(waiterJoined) })
					}
				},
			},
		})
		testutil.FailErr(t, "new core", err)
		t.Cleanup(func() {
			injectHashEIO.Store(false)
			blockFirstAttempt.Store(false)
			unblockOnce.Do(func() { close(unblockHook) })
			_ = c.ReleaseView()
		})

		qp := QualifiedPath{RootID: "r0", Rel: "hello.txt"}
		_, err = c.WriteFile(qp, []byte("modified-v2\n"), 0o644)
		testutil.FailErr(t, "WriteFile hello.txt", err)

		injectHashEIO.Store(true)
		blockFirstAttempt.Store(true)

		errCh1 := make(chan error, 1)
		go func() {
			errCh1 <- c.ReleaseView()
		}()

		select {
		case <-hookEntered:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for ReleaseView hash hook")
		}

		// Launch a second concurrent ReleaseView caller and wait for the explicit joined-attempt handshake.
		errCh2 := make(chan error, 1)
		go func() {
			errCh2 <- c.ReleaseView()
		}()
		select {
		case <-waiterJoined:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for concurrent ReleaseView caller to join attempt")
		}
		blockFirstAttempt.Store(false)
		unblockOnce.Do(func() { close(unblockHook) })

		err1 := <-errCh1
		err2 := <-errCh2
		if !errors.Is(err1, syscall.EIO) {
			t.Fatalf("primary ReleaseView caller expected EIO, got %v", err1)
		}
		if !errors.Is(err2, syscall.EIO) {
			t.Fatalf("concurrent ReleaseView caller expected EIO, got %v", err2)
		}

		// Verify a repeated ReleaseView call while fault persists still returns EIO (never nil).
		if err := c.ReleaseView(); !errors.Is(err, syscall.EIO) {
			t.Fatalf("repeated ReleaseView while EIO active expected EIO, got %v", err)
		}

		// Verify in-memory recovery state is still retained and readable.
		if ops := c.JournalOperations(); len(ops) == 0 {
			t.Fatalf("expected retained journal operations after failed ReleaseView")
		}
		if base, ok := c.CapturedBase(qp); !ok || base.Size == 0 {
			t.Fatalf("expected retained base capture after failed ReleaseView: ok=%v base=%+v", ok, base)
		}

		// Clear fault and retry ReleaseView; must succeed and persist journal.json.
		injectHashEIO.Store(false)
		testutil.FailErr(t, "retry ReleaseView after clearing EIO", c.ReleaseView())
		// Subsequent idempotent ReleaseView call after successful release returns nil.
		testutil.FailErr(t, "idempotent ReleaseView after success", c.ReleaseView())

		snap, err := c.LoadPersistedJournal()
		testutil.FailErr(t, "load persisted journal after retry", err)
		if len(snap.Changes) != 1 || snap.Changes[0].Path != qp {
			t.Fatalf("expected 1 persisted change for hello.txt, got %+v", snap.Changes)
		}
	})

	t.Run("journal_write_enospc_and_handle_sync_eio", func(t *testing.T) {
		rootDir := t.TempDir()
		backingDir := t.TempDir()
		testutil.FailErr(t, "write lower open.txt", os.WriteFile(filepath.Join(rootDir, "open.txt"), []byte("initial\n"), 0o644))

		var failHandleSync atomic.Bool
		var failJournalWrite atomic.Bool

		c, err := New(Config{
			ViewID:     "view-r4-sync-enospc",
			BackingDir: backingDir,
			Roots: []RootSpec{
				{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true},
			},
			FaultHooks: &FaultHooks{
				JournalSyncHook: func(phase string) error {
					if phase == "handle_sync" && failHandleSync.Load() {
						return syscall.EIO
					}
					if phase == "journal_tmp_write" && failJournalWrite.Load() {
						return syscall.ENOSPC
					}
					return nil
				},
			},
		})
		testutil.FailErr(t, "new core", err)
		t.Cleanup(func() {
			failHandleSync.Store(false)
			failJournalWrite.Store(false)
			_ = c.ReleaseView()
		})

		qp := QualifiedPath{RootID: "r0", Rel: "open.txt"}
		hid, _, err := c.Open(qp, OpenWriteOnly|OpenTruncate, 0)
		testutil.FailErr(t, "open open.txt", err)
		_, err = c.WriteHandle(hid, []byte("dirty-open-handle\n"), 0)
		testutil.FailErr(t, "write open.txt", err)

		// Leave handle open at ReleaseView time and fail both handle_sync and journal_tmp_write.
		failHandleSync.Store(true)
		failJournalWrite.Store(true)
		err = c.ReleaseView()
		if !errors.Is(err, syscall.EIO) || !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("expected joined EIO and ENOSPC from ReleaseView, got %v", err)
		}

		// Clear handle_sync failure so only journal_tmp_write fails on next retry.
		failHandleSync.Store(false)
		err = c.ReleaseView()
		if !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("expected ENOSPC on second ReleaseView attempt, got %v", err)
		}
		assertNoJournalTempFiles(t, filepath.Join(backingDir, "journal"))

		// Clear journal_tmp_write failure and retry ReleaseView; must succeed.
		failJournalWrite.Store(false)
		testutil.FailErr(t, "retry ReleaseView after clearing ENOSPC", c.ReleaseView())
		assertNoJournalTempFiles(t, filepath.Join(backingDir, "journal"))

		snap, err := c.LoadPersistedJournal()
		testutil.FailErr(t, "load persisted journal", err)
		if len(snap.Changes) != 1 || snap.Changes[0].Path != qp {
			t.Fatalf("expected persisted change for open.txt, got %+v", snap.Changes)
		}
	})
}

func TestReleaseAndSnapshotPropagatePostRenameDirSyncFailure(t *testing.T) {
	rootDir := t.TempDir()
	backingDir := t.TempDir()
	testutil.FailErr(t, "write lower doc.txt", os.WriteFile(filepath.Join(rootDir, "doc.txt"), []byte("v1\n"), 0o644))

	var failJournalDirSync atomic.Bool
	c, err := New(Config{
		ViewID:     "view-r4-dir-sync",
		BackingDir: backingDir,
		Roots: []RootSpec{
			{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true},
		},
		FaultHooks: &FaultHooks{
			JournalSyncHook: func(phase string) error {
				if phase == "journal_dir_sync" && failJournalDirSync.Load() {
					return syscall.EIO
				}
				return nil
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() {
		failJournalDirSync.Store(false)
		_ = c.ReleaseView()
	})

	qp := QualifiedPath{RootID: "r0", Rel: "doc.txt"}
	_, err = c.WriteFile(qp, []byte("v2\n"), 0o644)
	testutil.FailErr(t, "WriteFile doc.txt", err)

	failJournalDirSync.Store(true)
	if _, err := c.SnapshotJournal(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("expected SnapshotJournal to propagate post-rename dir sync EIO, got %v", err)
	}
	if err := c.ReleaseView(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("expected ReleaseView to propagate post-rename dir sync EIO, got %v", err)
	}

	failJournalDirSync.Store(false)
	testutil.FailErr(t, "ReleaseView after clearing journal_dir_sync EIO", c.ReleaseView())
	snap, err := c.LoadPersistedJournal()
	testutil.FailErr(t, "load persisted journal", err)
	if len(snap.Changes) != 1 {
		t.Fatalf("expected 1 persisted change, got %d", len(snap.Changes))
	}
}

func TestConcurrentSnapshotJournalAndRelease(t *testing.T) {
	rootDir := t.TempDir()
	backingDir := t.TempDir()
	testutil.FailErr(t, "write lower shared.txt", os.WriteFile(filepath.Join(rootDir, "shared.txt"), []byte("seed\n"), 0o644))

	c, err := New(Config{
		ViewID:     "view-r4-concurrent-snap",
		BackingDir: backingDir,
		Roots: []RootSpec{
			{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { _ = c.ReleaseView() })

	qp := QualifiedPath{RootID: "r0", Rel: "shared.txt"}
	_, err = c.WriteFile(qp, []byte("updated\n"), 0o644)
	testutil.FailErr(t, "WriteFile shared.txt", err)

	const workers = 8
	const iterations = 10
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if _, sErr := c.SnapshotJournal(); sErr != nil {
					errCh <- sErr
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for sErr := range errCh {
		testutil.FailErr(t, "concurrent SnapshotJournal", sErr)
	}

	testutil.FailErr(t, "ReleaseView after concurrent snapshots", c.ReleaseView())
	assertNoJournalTempFiles(t, filepath.Join(backingDir, "journal"))

	snap, err := c.LoadPersistedJournal()
	testutil.FailErr(t, "load persisted journal", err)
	if len(snap.Changes) != 1 || snap.Changes[0].Path != qp {
		t.Fatalf("unexpected persisted changes: %+v", snap.Changes)
	}
}

func assertNoJournalTempFiles(t *testing.T, journalDir string) {
	t.Helper()
	entries, err := os.ReadDir(journalDir)
	testutil.FailErr(t, "read journal dir", err)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "journal.json.tmp") {
			t.Fatalf("leftover temporary journal file found: %s", e.Name())
		}
	}
}
