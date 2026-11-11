package delegations

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A view's working log records every namespace change as it happens, so settling a view that was
// never released reports exactly the paths it changed. Only a view whose log is gone reports its
// changes since the last barrier as unknown.
func TestSettlingAnUnreleasedViewReportsTheScopeItsWorkingLogNames(t *testing.T) {
	keep := layeredfs.QualifiedPath{RootID: fixtureRootID, Rel: "keep.txt"}
	t.Run("a_deletion_after_the_checkpoint", func(t *testing.T) {
		f := newEngineFixture(t)
		task := f.claimMountedWorker()
		view := f.liveView(task.WorkspaceRoot)
		mutate(t, view, "checkpoint-A\n")
		f.publish(task, view, "cp-A")
		testutil.FailErr(t, "delete after the barrier", view.Core.Unlink(keep, false))

		report := f.boot().Recovery.RecoverWorkerRuntime(t.Context(), task.ID, false)
		if !report.Deletable() || report.Interrupted == nil || report.Interrupted.UnknownScope || !slices.Contains(report.Interrupted.AffectedPaths, keep) {
			t.Fatalf("the logged deletion must be reported: %+v", report)
		}
		if quarantineHolds(t, report.Interrupted.QuarantineDir, "checkpoint-A\n") {
			t.Fatal("bytes already in the checkpoint are not copied again")
		}
	})
	t.Run("an_edit_after_the_checkpoint", func(t *testing.T) {
		f := newEngineFixture(t)
		task := f.claimMountedWorker()
		view := f.liveView(task.WorkspaceRoot)
		mutate(t, view, "checkpoint-A\n")
		f.publish(task, view, "cp-A")
		mutate(t, view, "unsealed-after-A\n")

		report := f.boot().Recovery.RecoverWorkerRuntime(t.Context(), task.ID, false)
		if !report.Deletable() || report.Interrupted == nil || report.Interrupted.UnknownScope || !slices.Contains(report.Interrupted.AffectedPaths, primaryPath) {
			t.Fatalf("report %+v", report)
		}
		if !quarantineHolds(t, report.Interrupted.QuarantineDir, "unsealed-after-A\n") {
			t.Fatal("bytes on disk are preserved")
		}
	})
	t.Run("a_lost_log_leaves_the_scope_unknown", func(t *testing.T) {
		f := newEngineFixture(t)
		task := f.claimMountedWorker()
		view := f.liveView(task.WorkspaceRoot)
		mutate(t, view, "checkpoint-A\n")
		f.publish(task, view, "cp-A")
		testutil.FailErr(t, "delete after the barrier", view.Core.Unlink(keep, false))
		testutil.FailErr(t, "lose the log", os.Remove(filepath.Join(view.BackingDir, "journal", "working.log")))

		report := f.boot().Recovery.RecoverWorkerRuntime(t.Context(), task.ID, false)
		if !report.Deletable() || report.Interrupted == nil || !report.Interrupted.UnknownScope {
			t.Fatalf("lost records must be reported: %+v", report)
		}
		if report.Interrupted.InterruptedPhase != recovery.PhaseActiveMutation || len(report.Interrupted.AffectedPaths) != 0 {
			t.Fatalf("record %+v", report.Interrupted)
		}
	})
	t.Run("a_released_view_reports_nothing", func(t *testing.T) {
		f := newEngineFixture(t)
		task := f.claimMountedWorker()
		view := f.liveView(task.WorkspaceRoot)
		mutate(t, view, "checkpoint-A\n")
		f.publish(task, view, "cp-A")
		testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))
		if records, err := f.engine.Recovery.ListInterruptedRecords(task.ID); err != nil || len(records) != 0 {
			t.Fatalf("records %+v err=%v", records, err)
		}
	})
}
