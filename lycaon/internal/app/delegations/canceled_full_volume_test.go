//go:build darwin || linux

package delegations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// fillVolume writes until the volume refuses another byte. Each size stops at ENOSPC, so what
// remains free is smaller than the smallest allocation the filesystem will make.
func fillVolume(t *testing.T, dir string) {
	t.Helper()
	testutil.FailErr(t, "fill dir", os.MkdirAll(dir, 0o755))
	chunk := make([]byte, 1<<20)
	for i := 0; len(chunk) > 0; chunk = chunk[:len(chunk)/2] {
		for ; ; i++ {
			err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("fill-%d", i)), chunk, 0o644)
			if errors.Is(err, syscall.ENOSPC) {
				break
			}
			testutil.FailErr(t, "fill volume", err)
		}
		i++
	}
}

// A real volume with no free blocks. The database lives elsewhere: this proves what retention
// itself needs from a full volume, not what SQLite needs.
func TestCancelFinishesOnAFullVolume(t *testing.T) {
	if ranInOwnMountNamespace(t) {
		return
	}
	f := newEngineFixture(t)
	// Branches must stay at their installation path, so the volume is mounted there and the
	// coordinator keeps its state on it too, as it shares a volume with branches in production.
	volume := enginepaths.WorkerBranchesRootUnder(f.dataRoot)
	testutil.FailErr(t, "mount point", os.MkdirAll(volume, 0o755))
	mountSmallVolume(t, volume)
	f.stateRoot = filepath.Join(volume, "state")
	f.boot()
	task := f.claimMaterializedWorker()
	writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
	fillVolume(t, filepath.Join(task.WorkspaceRoot, "filled"))
	if err := os.WriteFile(filepath.Join(volume, "probe"), []byte("x"), 0o644); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("volume is not full: %v", err)
	}

	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))

	if got, _ := f.engine.Queue.Get(task.ID); got.Status != api.WorkerStatusCanceled {
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
