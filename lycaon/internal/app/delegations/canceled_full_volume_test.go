//go:build darwin || linux

package delegations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// fillVolume writes until the volume refuses another byte. Each size stops at ENOSPC, so what
// remains free is smaller than the smallest allocation the filesystem will make.
func fillVolume(t *testing.T, dir, prefix string) {
	t.Helper()
	testutil.FailErr(t, "fill dir", os.MkdirAll(dir, 0o755))
	chunk := make([]byte, 1<<20)
	for i := 0; len(chunk) > 0; chunk = chunk[:len(chunk)/2] {
		for ; ; i++ {
			err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d", prefix, i)), chunk, 0o644)
			if errors.Is(err, syscall.ENOSPC) {
				break
			}
			testutil.FailErr(t, "fill volume", err)
		}
		i++
	}
}

// A real volume with no free blocks, through the production cancel path.
func TestCancelFinishesOnAFullVolume(t *testing.T) {
	if ranInOwnMountNamespace(t) {
		return
	}
	t.Run("database_elsewhere", func(t *testing.T) {
		f := newEngineFixture(t)
		// Branches must stay at their installation path, so the volume is mounted there and the
		// coordinator keeps its state on it too, as it shares a volume with branches in production.
		volume := enginepaths.WorkerBranchesRootUnder(f.dataRoot)
		testutil.FailErr(t, "mount point", os.MkdirAll(volume, 0o755))
		mountSmallVolume(t, volume)
		f.stateRoot = filepath.Join(volume, "state")
		f.boot()
		assertCanceledAndRetained(t, f, claimAndFill(t, f, volume))
	})
	t.Run("database_on_the_volume", func(t *testing.T) {
		volume := filepath.Join(t.TempDir(), "volume")
		testutil.FailErr(t, "mount point", os.Mkdir(volume, 0o755))
		mountSmallVolume(t, volume)
		f := newEngineFixtureOn(t, testdbfixture.OpenPath(t, filepath.Join(volume, "store.db")))
		task := claimAndFill(t, f, volume)

		// An empty write-ahead log leaves no slack, so the very first cancellation write is refused.
		_, err := f.database.ExecContext(t.Context(), "PRAGMA wal_checkpoint(TRUNCATE)")
		testutil.FailErr(t, "truncate write-ahead log", err)
		fillVolume(t, filepath.Join(task.WorkspaceRoot, "filled"), "after-checkpoint")
		_, err = f.database.ExecContext(t.Context(), `UPDATE worker_jobs SET created_at = created_at WHERE id = ?`, task.ID)
		if !db.IsDiskFull(err) {
			t.Fatalf("database write on the full volume: %v", err)
		}
		assertCanceledAndRetained(t, f, task)
	})
	t.Run("database_on_the_volume_with_no_reserve_left", func(t *testing.T) {
		volume := filepath.Join(t.TempDir(), "volume")
		testutil.FailErr(t, "mount point", os.Mkdir(volume, 0o755))
		mountSmallVolume(t, volume)
		f := newEngineFixtureOn(t, testdbfixture.OpenPath(t, filepath.Join(volume, "store.db")))
		task := claimAndFill(t, f, volume)
		_, err := f.database.ExecContext(t.Context(), "PRAGMA wal_checkpoint(TRUNCATE)")
		testutil.FailErr(t, "truncate write-ahead log", err)
		if !f.engine.Workspace.ReleaseSpaceReserve() {
			t.Fatal("the coordinator set no reserve aside")
		}
		fillVolume(t, filepath.Join(task.WorkspaceRoot, "filled"), "reserve-gone")

		if err := f.engine.Queue.Cancel(t.Context(), task.ID, nil); !db.IsDiskFull(err) {
			t.Fatalf("cancel with nothing left to free: %v", err)
		}
		if got, _ := f.engine.Queue.Get(task.ID); got.Status.IsTerminal() {
			t.Fatalf("cancel committed: %+v", got)
		}
		if got := f.readBranch(task.WorkspaceRoot, "app.txt"); got != "unpromoted edit\n" {
			t.Fatalf("tree lost work: %q", got)
		}
		// Closing the store writes too.
		testutil.FailErr(t, "free the volume", os.RemoveAll(filepath.Join(task.WorkspaceRoot, "filled")))
	})
}

func claimAndFill(t *testing.T, f *engineFixture, volume string) *api.WorkerTask {
	t.Helper()
	task := f.claimMaterializedWorker()
	writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
	fillVolume(t, filepath.Join(task.WorkspaceRoot, "filled"), "first")
	if err := os.WriteFile(filepath.Join(volume, "probe"), []byte("x"), 0o644); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("volume is not full: %v", err)
	}
	return task
}

func assertCanceledAndRetained(t *testing.T, f *engineFixture, task *api.WorkerTask) {
	t.Helper()
	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))

	got, ok := f.engine.Queue.Get(task.ID)
	if !ok || got.Status != api.WorkerStatusCanceled || got.Result == nil || got.Result.ChangeReport == nil {
		t.Fatalf("cancel: %+v", got)
	}
	if _, err := os.Lstat(task.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tree survives cancellation: %v", err)
	}
	if got := retainedFile(t, onlyRecord(t, f.engine, task.ID), "app.txt"); got != "unpromoted edit\n" {
		t.Fatalf("retained %q", got)
	}

	// The next engine finds the room gone and takes back only what is free.
	f.boot()
	if records, err := f.engine.Recovery.ListInterruptedRecords(task.ID); err != nil || len(records) != 1 {
		t.Fatalf("records after restart %+v err=%v", records, err)
	}
}
