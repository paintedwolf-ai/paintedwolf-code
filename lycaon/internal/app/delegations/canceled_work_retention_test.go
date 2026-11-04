package delegations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// claimMaterializedWorker takes the ordinary claim path with the copy adapter selected.
func (f *engineFixture) claimMaterializedWorker() *api.WorkerTask {
	f.t.Helper()
	ctx := f.t.Context()
	f.engine.Workspace.WithSelector(adapter.NewSelector("", nil, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone)))
	_, err := f.engine.Queue.Enqueue(ctx, api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: f.project, WorkspaceRootID: fixtureRootID,
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		AgentType: "implementer", Prompt: "Change app.txt", Brief: "Change app.txt",
	})
	testutil.FailErr(f.t, "enqueue", err)
	claimed, err := f.engine.Queue.ClaimNext(ctx, worker.ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(f.t, "claim next", err)
	claimed, err = f.engine.Queue.ClaimWorkerBranch(ctx, claimed.ID)
	testutil.FailErr(f.t, "claim branch", err)
	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(claimed.WorkspaceRoot))
	testutil.FailErr(f.t, "job meta", err)
	if meta.PreparationKind == adapter.PreparationMounted {
		f.t.Fatalf("expected a materialized tree: %+v", meta)
	}
	return claimed
}

func writeTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(path, []byte(content), 0o644))
}

func retainedFile(t *testing.T, record recovery.InterruptedWorkingRecord, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, filepath.FromSlash(rel)))
	testutil.FailErr(t, "read retained "+rel, err)
	return string(raw)
}

func retainedAbsent(t *testing.T, record recovery.InterruptedWorkingRecord, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s must not be retained: %v", rel, err)
	}
}

func onlyRecord(t *testing.T, engine *Runtime, jobID string) recovery.InterruptedWorkingRecord {
	t.Helper()
	records, err := engine.Recovery.ListInterruptedRecords(jobID)
	testutil.FailErr(t, "interrupted records", err)
	if len(records) != 1 {
		t.Fatalf("interrupted records %+v", records)
	}
	return records[0]
}

func TestCancelRetainsAMaterializedWorkersChangedBytesThenDeletesTheTree(t *testing.T) {
	f := newEngineFixture(t)
	writeTreeFile(t, f.project, "gone.txt", "deleted by the worker\n")
	writeTreeFile(t, f.project, "dir/inner.txt", "under a replaced directory\n")
	writeTreeFile(t, f.project, "esc/inner.txt", "under a directory replaced by a link\n")
	outside := t.TempDir()
	writeTreeFile(t, outside, "inner.txt", "outside the tree\n")

	task := f.claimMaterializedWorker()
	root := task.WorkspaceRoot
	writeTreeFile(t, root, "app.txt", "unpromoted edit\n")
	writeTreeFile(t, root, "new/deep/file.txt", "unpromoted new file\n")
	writeTreeFile(t, root, "unreadable.txt", "worker removed its read bit\n")
	testutil.FailErr(t, "chmod", os.Chmod(filepath.Join(root, "unreadable.txt"), 0))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(root, "gone.txt")))
	testutil.FailErr(t, "drop dir", os.RemoveAll(filepath.Join(root, "dir")))
	writeTreeFile(t, root, "dir", "file over a directory\n")
	testutil.FailErr(t, "drop esc", os.RemoveAll(filepath.Join(root, "esc")))
	testutil.FailErr(t, "link esc", os.Symlink(outside, filepath.Join(root, "esc")))
	testutil.FailErr(t, "link", os.Symlink("app.txt", filepath.Join(root, "link")))

	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))

	if got, _ := f.engine.Queue.Get(task.ID); got.Status != api.WorkerStatusCanceled {
		t.Fatalf("cancel: %+v", got)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tree survives cancellation: %v", err)
	}
	record := onlyRecord(t, f.engine, task.ID)
	if record.InterruptedPhase != recovery.PhaseRelease || record.InterruptedOp != "cancel" || record.UnknownScope {
		t.Fatalf("record %+v", record)
	}
	unreadable, err := os.Lstat(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, "unreadable.txt"))
	testutil.FailErr(t, "stat unreadable", err)
	if unreadable.Mode().Perm() != 0 {
		t.Fatalf("mode not kept: %v", unreadable.Mode())
	}
	testutil.FailErr(t, "reopen unreadable", os.Chmod(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, "unreadable.txt"), 0o600))
	for rel, want := range map[string]string{
		"app.txt": "unpromoted edit\n", "new/deep/file.txt": "unpromoted new file\n",
		"unreadable.txt": "worker removed its read bit\n", "dir": "file over a directory\n",
	} {
		if got := retainedFile(t, record, rel); got != want {
			t.Fatalf("%s retained %q, want %q", rel, got, want)
		}
	}
	if target, err := os.Readlink(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, "link")); err != nil || target != "app.txt" {
		t.Fatalf("retained link %q err=%v", target, err)
	}
	retainedAbsent(t, record, "keep.txt")
	retainedAbsent(t, record, "gone.txt")
	if target, err := os.Readlink(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, "esc")); err != nil || target != outside {
		t.Fatalf("a link is retained as a link, not followed: %q err=%v", target, err)
	}
	for _, rel := range []string{"app.txt", "gone.txt", "dir/inner.txt", "esc/inner.txt", "new/deep/file.txt", "link"} {
		if !slices.Contains(record.AffectedPaths, layeredfs.QualifiedPath{RootID: fixtureRootID, Rel: rel}) {
			t.Fatalf("%s missing from affected paths %+v", rel, record.AffectedPaths)
		}
	}
	if slices.Contains(record.AffectedPaths, layeredfs.QualifiedPath{RootID: fixtureRootID, Rel: "keep.txt"}) {
		t.Fatalf("an unchanged path is not affected: %+v", record.AffectedPaths)
	}
	inventory, err := f.engine.Recovery.LoadInventory()
	testutil.FailErr(t, "inventory", err)
	found := false
	for _, entry := range inventory.Entries {
		if entry.Bucket == recovery.BucketInterruptedWorkingData && entry.WorkerID == task.ID {
			found = entry.Path == record.QuarantineDir && entry.Bytes > 0
		}
	}
	if !found {
		t.Fatalf("inventory lacks the retained work: %+v", inventory.Entries)
	}
}

func TestCancelWithNoChangesRetainsNothing(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMaterializedWorker()
	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))
	if records, err := f.engine.Recovery.ListInterruptedRecords(task.ID); err != nil || len(records) != 0 {
		t.Fatalf("records %+v err=%v", records, err)
	}
	if _, err := os.Lstat(task.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tree survives cancellation: %v", err)
	}
}

// The overlay budget limits what review can seal. It must never make a cancel lose work or stick.
func TestCancelRetainsChangesPastTheOverlayBudget(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMaterializedWorker()
	const files = 10_001
	for i := range files {
		writeTreeFile(t, task.WorkspaceRoot, fmt.Sprintf("many/%d/f%d.txt", i%64, i), "x")
	}
	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))
	if record := onlyRecord(t, f.engine, task.ID); len(record.AffectedPaths) != files {
		t.Fatalf("retained %d paths, want %d", len(record.AffectedPaths), files)
	}
}

func assertRetentionBlocked(t *testing.T, f *engineFixture, task *api.WorkerTask, err error) {
	t.Helper()
	var blocked *recovery.RuntimeBlockedError
	var unresolved *recovery.UnresolvedSettlementError
	if !errors.As(err, &blocked) || !errors.As(err, &unresolved) {
		t.Fatalf("expected an unresolved settlement: %v", err)
	}
	if blocked.Report.Disposition != recovery.RuntimeUnresolved || blocked.Report.Reason != recovery.ReasonSalvageFailed {
		t.Fatalf("report %+v", blocked.Report)
	}
	if !unresolved.RetainedCustody || !slices.Contains(unresolved.RetainedStorageRefs, task.WorkspaceRoot) {
		t.Fatalf("unresolved %+v", unresolved)
	}
	if got, _ := f.engine.Queue.Get(task.ID); got.Status.IsTerminal() {
		t.Fatalf("cancel committed over failed retention: %+v", got)
	}
	if got := f.readBranch(task.WorkspaceRoot, "app.txt"); got != "unpromoted edit\n" {
		t.Fatalf("tree lost work: %q", got)
	}
	if records, err := f.engine.Recovery.ListInterruptedRecords(task.ID); err != nil || len(records) != 0 {
		t.Fatalf("a failed retention must not report preserved work: %+v err=%v", records, err)
	}
}

func TestFailedRetentionKeepsTheTreeAndReportsUnresolvedThroughEveryCancelPath(t *testing.T) {
	for _, path := range []string{"queue_cancel", "run_stop"} {
		t.Run(path, func(t *testing.T) {
			f := newEngineFixture(t)
			diskFull := true
			f.writeHook = func(_, name string, _ []byte) error {
				if diskFull && strings.HasPrefix(name, "intr-") {
					return syscall.ENOSPC
				}
				return nil
			}
			f.boot()
			task := f.claimMaterializedWorker()
			writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
			const runID = "run-under-stop"
			_, err := f.database.ExecContext(t.Context(), `UPDATE worker_jobs SET workflow_run_id = ? WHERE id = ?`, runID, task.ID)
			testutil.FailErr(t, "bind run", err)
			cancel := func() error {
				if path == "queue_cancel" {
					return f.engine.Queue.Cancel(t.Context(), task.ID, nil)
				}
				return (&worker.RunStopService{Queue: f.engine.Queue}).CancelWorkersByRunID(t.Context(), runID, "stop")
			}

			assertRetentionBlocked(t, f, task, cancel())

			diskFull = false
			testutil.FailErr(t, "retry", cancel())
			if got, _ := f.engine.Queue.Get(task.ID); got.Status != api.WorkerStatusCanceled {
				t.Fatalf("retry: %+v", got)
			}
			if got := retainedFile(t, onlyRecord(t, f.engine, task.ID), "app.txt"); got != "unpromoted edit\n" {
				t.Fatalf("retained %q", got)
			}
			if _, err := os.Lstat(task.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("tree survives retry: %v", err)
			}
		})
	}
}

// A baseline that may never return, including a job canceled during preparation, must not stop
// the cancel. Nothing can name the changes, so the whole tree is kept and says so.
func TestCancelWithNoUsableBaselineRetainsTheWholeTreeWithUnknownScope(t *testing.T) {
	for name, breakBaseline := range map[string]func(*testing.T, string){
		"missing": func(t *testing.T, path string) {
			testutil.FailErr(t, "remove baseline", os.Remove(path))
		},
		"unreadable": func(t *testing.T, path string) {
			testutil.FailErr(t, "remove baseline", os.Remove(path))
			testutil.FailErr(t, "corrupt baseline", os.WriteFile(path, []byte("not a manifest"), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEngineFixture(t)
			task := f.claimMaterializedWorker()
			writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
			breakBaseline(t, task.WorkspaceBaselinePath)
			before, err := os.Lstat(filepath.Join(task.WorkspaceRoot, "keep.txt"))
			testutil.FailErr(t, "stat keep", err)

			testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))

			if got, _ := f.engine.Queue.Get(task.ID); got.Status != api.WorkerStatusCanceled {
				t.Fatalf("cancel: %+v", got)
			}
			if _, err := os.Lstat(task.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("tree survives cancellation: %v", err)
			}
			record := onlyRecord(t, f.engine, task.ID)
			if !record.UnknownScope || record.InterruptedOp != "cancel" || len(record.AffectedPaths) != 0 {
				t.Fatalf("record %+v", record)
			}
			if raw, err := os.ReadFile(filepath.Join(record.QuarantineDir, "tree", "app.txt")); err != nil || string(raw) != "unpromoted edit\n" {
				t.Fatalf("retained %q err=%v", raw, err)
			}
			kept, err := os.Lstat(filepath.Join(record.QuarantineDir, "tree", "keep.txt"))
			testutil.FailErr(t, "stat retained keep", err)
			if !os.SameFile(before, kept) {
				t.Fatal("the whole tree is linked, not copied")
			}
		})
	}
}

func TestCancelDuringPreparationRetainsThePartialTree(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMaterializedWorker()
	testutil.FailErr(t, "remove baseline", os.Remove(task.WorkspaceBaselinePath))
	testutil.FailErr(t, "partial seed", os.Remove(filepath.Join(task.WorkspaceRoot, "keep.txt")))
	testutil.FailErr(t, "closed dir", os.Mkdir(filepath.Join(task.WorkspaceRoot, "closed"), 0o755))
	writeTreeFile(t, task.WorkspaceRoot, "closed/inner.txt", "inside a closed directory\n")
	testutil.FailErr(t, "close dir", os.Chmod(filepath.Join(task.WorkspaceRoot, "closed"), 0))
	testutil.FailErr(t, "link", os.Symlink("app.txt", filepath.Join(task.WorkspaceRoot, "link")))

	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))

	record := onlyRecord(t, f.engine, task.ID)
	if raw, err := os.ReadFile(filepath.Join(record.QuarantineDir, "tree", "closed", "inner.txt")); err != nil || string(raw) != "inside a closed directory\n" {
		t.Fatalf("retained %q err=%v", raw, err)
	}
	if target, err := os.Readlink(filepath.Join(record.QuarantineDir, "tree", "link")); err != nil || target != "app.txt" {
		t.Fatalf("retained link %q err=%v", target, err)
	}
}

// A canceled caller says nothing about the baseline: the worker stays unresolved for a retry.
func TestAnExpiredCallerDoesNotRetainTheWholeTree(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMaterializedWorker()
	writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := f.engine.Workspace.RetainCanceledWork(ctx, task.ID, task.WorkspaceRoot, task.WorkspaceBaselinePath)
	assertRetentionBlocked(t, f, task, err)
}

func TestWholeTreeRetentionFailureKeepsTheTree(t *testing.T) {
	f := newEngineFixture(t)
	f.faults = func(_ recovery.TransitionPhase, at string) error {
		if at == "link_changed_file" {
			return syscall.EIO
		}
		return nil
	}
	f.boot()
	task := f.claimMaterializedWorker()
	writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
	testutil.FailErr(t, "remove baseline", os.Remove(task.WorkspaceBaselinePath))

	assertRetentionBlocked(t, f, task, f.engine.Queue.Cancel(t.Context(), task.ID, nil))
}

// A tree with no coordinator to hold its work is never deleted silently.
func TestCancelWithoutARecoveryCoordinatorStaysUnresolved(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMaterializedWorker()
	writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
	unrecorded := workspace.NewManager(f.engine.BranchRoot, f.engine.SeedRoot)
	f.engine.Queue.SetWorkerWorkspaceManager(unrecorded)

	err := f.engine.Queue.Cancel(t.Context(), task.ID, nil)
	assertRetentionBlocked(t, f, task, err)
}

// Linking needs no file data, so a volume with no room for the bytes cannot stop a cancel.
func TestCancelNeverCopiesWhenLinkingWorks(t *testing.T) {
	f := newEngineFixture(t)
	f.faults = func(_ recovery.TransitionPhase, at string) error {
		if at == "copy_changed_file" {
			return syscall.ENOSPC
		}
		return nil
	}
	f.boot()
	task := f.claimMaterializedWorker()
	writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
	testutil.FailErr(t, "chmod", os.Chmod(filepath.Join(task.WorkspaceRoot, "app.txt"), 0o751))
	before, err := os.Lstat(filepath.Join(task.WorkspaceRoot, "app.txt"))
	testutil.FailErr(t, "stat", err)

	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))

	record := onlyRecord(t, f.engine, task.ID)
	kept, err := os.Lstat(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, "app.txt"))
	testutil.FailErr(t, "stat retained", err)
	if !os.SameFile(before, kept) || kept.Mode().Perm() != 0o751 {
		t.Fatalf("retained %v, source %v", kept.Mode(), before.Mode())
	}
}

// Where a link cannot exist the bytes are copied with their mode, and a copy that does not fit
// is an honest failure.
func TestCancelCopiesWhenLinkingIsImpossible(t *testing.T) {
	for name, cause := range map[string]error{"cross_device": syscall.EXDEV, "link_limit": syscall.EMLINK} {
		t.Run(name, func(t *testing.T) {
			f := newEngineFixture(t)
			copyFails := true
			f.faults = func(_ recovery.TransitionPhase, at string) error {
				switch {
				case at == "link_changed_file":
					return cause
				case at == "copy_changed_file" && copyFails:
					return syscall.ENOSPC
				}
				return nil
			}
			f.boot()
			task := f.claimMaterializedWorker()
			writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
			testutil.FailErr(t, "chmod", os.Chmod(filepath.Join(task.WorkspaceRoot, "app.txt"), 0o751))
			before, err := os.Lstat(filepath.Join(task.WorkspaceRoot, "app.txt"))
			testutil.FailErr(t, "stat", err)

			assertRetentionBlocked(t, f, task, f.engine.Queue.Cancel(t.Context(), task.ID, nil))

			copyFails = false
			testutil.FailErr(t, "retry", f.engine.Queue.Cancel(t.Context(), task.ID, nil))
			record := onlyRecord(t, f.engine, task.ID)
			if got := retainedFile(t, record, "app.txt"); got != "unpromoted edit\n" {
				t.Fatalf("retained %q", got)
			}
			kept, err := os.Lstat(filepath.Join(record.QuarantineDir, "changed", fixtureRootID, "app.txt"))
			testutil.FailErr(t, "stat retained", err)
			if os.SameFile(before, kept) || kept.Mode().Perm() != 0o751 {
				t.Fatalf("a copy with its mode expected: %v", kept.Mode())
			}
		})
	}
}

// [process-crash evidence, not physical power-loss proof] A retention finished by one engine and
// committed by the next keeps one record and the same bytes.
func TestRetentionRetriesAcrossARestartWithoutDuplicating(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMaterializedWorker()
	writeTreeFile(t, task.WorkspaceRoot, "app.txt", "unpromoted edit\n")
	testutil.FailErr(t, "retain", f.engine.Workspace.RetainCanceledWork(t.Context(), task.ID, task.WorkspaceRoot, task.WorkspaceBaselinePath))

	engine := f.boot()
	testutil.FailErr(t, "cancel on the next engine", engine.Queue.Cancel(t.Context(), task.ID, nil))
	if got := retainedFile(t, onlyRecord(t, engine, task.ID), "app.txt"); got != "unpromoted edit\n" {
		t.Fatalf("retained %q", got)
	}
}
