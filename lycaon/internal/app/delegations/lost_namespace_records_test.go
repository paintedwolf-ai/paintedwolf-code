package delegations

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Namespace records persist at a barrier. A deletion after the last one leaves no trace on disk,
// so recovery must say the view's history is unknown rather than present what it has as complete.
func TestRestartReportsNamespaceChangesSinceTheBarrierAsUnknownScope(t *testing.T) {
	t.Run("nothing_on_disk_differs", func(t *testing.T) {
		f := newEngineFixture(t)
		task := f.claimMountedWorker()
		view := f.liveView(task.WorkspaceRoot)
		mutate(t, view, "checkpoint-A\n")
		f.publish(task, view, "cp-A")
		testutil.FailErr(t, "delete after the barrier", view.Core.Unlink(layeredfs.QualifiedPath{RootID: fixtureRootID, Rel: "keep.txt"}, false))

		engine := f.boot()
		report := engine.Recovery.RecoverWorkerRuntime(t.Context(), task.ID)
		if !report.Deletable() || report.Interrupted == nil || !report.Interrupted.UnknownScope {
			t.Fatalf("lost records must be reported: %+v", report)
		}
		if report.Interrupted.InterruptedPhase != recovery.PhaseActiveMutation || len(report.Interrupted.AffectedPaths) != 0 {
			t.Fatalf("record %+v", report.Interrupted)
		}
		if quarantineHolds(t, report.Interrupted.QuarantineDir, "checkpoint-A\n") {
			t.Fatal("bytes already in the checkpoint are not copied again")
		}
	})
	t.Run("older_records_do_not_make_the_scope_known", func(t *testing.T) {
		f := newEngineFixture(t)
		task := f.claimMountedWorker()
		view := f.liveView(task.WorkspaceRoot)
		mutate(t, view, "checkpoint-A\n")
		f.publish(task, view, "cp-A")
		mutate(t, view, "unsealed-after-A\n")
		testutil.FailErr(t, "journal begin", f.engine.Recovery.RecordTransition(task.ID, view.ViewID, recovery.PhaseActiveMutation, recovery.StateBegin, "cp-A", "write",
			[]layeredfs.QualifiedPath{primaryPath}, false))

		engine := f.boot()
		report := engine.Recovery.RecoverWorkerRuntime(t.Context(), task.ID)
		if !report.Deletable() || report.Interrupted == nil || !report.Interrupted.UnknownScope || !slices.Contains(report.Interrupted.AffectedPaths, primaryPath) {
			t.Fatalf("report %+v", report)
		}
		if !quarantineHolds(t, report.Interrupted.QuarantineDir, "unsealed-after-A\n") {
			t.Fatal("bytes on disk are preserved whatever the records say")
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
