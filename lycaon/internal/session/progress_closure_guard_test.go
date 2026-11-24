package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newClosureGuardManager(t *testing.T, store progress.Store) *Host {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr, _ := newTestManager(t)
	mgr.SetRejectFormatter(rejectFmt)
	mgr.SetProgressStore(store)
	mgr.SetOARPipeline(testCoordinatorPreInvokePipeline(t), oar.NewRenderer(rejectFmt, nil))
	return mgr
}

func testCoordinatorPreInvokePipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	catalogPath := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallFile(catalogPath))
	schemaDir := filepath.Join("..", "..", "..", "schemas")
	loader, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadStock", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreInvoke)
	return pipeline
}

func closureGuardPreInvoke(t *testing.T, mgr *Host, sess *api.Session, rootID, tool string) (bool, error) {
	t.Helper()
	snap, err := mgr.RewindRuntime.Progress.Snapshot(t.Context(), rootID)
	testutil.FailErr(t, "snapshot", err)
	baseline, armed := mgr.Coordinator.ProgressClosure.Baseline(rootID)
	reject, blocked, err := mgr.ToolPolicy.Block(t.Context(), oar.AnchorCoordinatorPreInvoke, sess, tool, nil, func(gc *oar.GuardContext) error {
		guard.ObserveProgressReconciliationBeforeDispatch(sess, snap.OpenStepCount(), tool, baseline, armed, gc)
		return nil
	})
	if err != nil {
		return false, err
	}
	if blocked {
		if reject != nil {
			return false, reject
		}
		return true, nil
	}
	return false, nil
}

func TestProgressClosureRejectWhenArmedAndNothingClosed(t *testing.T) {
	store := progresstest.New(t, "root-1")
	progresstest.SeedRows(t, store, "root-1",
		progress.RowSpec{Label: "step a", State: "done"},
		progress.RowSpec{Label: "step b"},
		progress.RowSpec{Label: "step c"},
	)
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}

	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	skip, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err == nil || skip {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok {
		t.Fatalf("err = %T want a refusal", err)
	}
	if refusal.Code() != "PROGRESS_RECONCILIATION_REQUIRED" {
		t.Fatalf("refusal code = %q", refusal.Code())
	}
	if !strings.Contains(refusal.Body, "PROGRESS_RECONCILIATION_REQUIRED") {
		t.Fatalf("body = %q", refusal.Body)
	}
	if strings.Contains(refusal.Body, "job-1") {
		t.Fatalf("body = %q names the job", refusal.Body)
	}
}

func TestProgressClosureAccumulatesCompletedWork(t *testing.T) {
	store := progresstest.New(t, "root-1")
	progresstest.SeedRows(t, store, "root-1",
		progress.RowSpec{Label: "step a"},
		progress.RowSpec{Label: "step b"},
	)
	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-2")
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	baseline, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1")
	if !armed || len(baseline.JobIDs) != 2 || baseline.JobIDs[0] != "job-1" || baseline.JobIDs[1] != "job-2" || len(baseline.Settled) != 2 {
		t.Fatalf("baseline = %+v armed=%v want job-1 then job-2 once each", baseline, armed)
	}
}

func TestProgressClosureRejectBlocksWriteWhenArmed(t *testing.T) {
	store := progresstest.New(t, "root-1")
	progresstest.SeedRows(t, store, "root-1", progress.RowSpec{Label: "step a"})
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	skip, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "write")
	if err == nil || skip {
		t.Fatalf("write should reject when armed; skip=%v err=%v", skip, err)
	}
}

func TestProgressClosureRejectAllowsReadWhenArmed(t *testing.T) {
	store := progresstest.New(t, "root-1")
	progresstest.SeedRows(t, store, "root-1", progress.RowSpec{Label: "step a"})
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "read")
	if err != nil || block {
		t.Fatalf("read must allow; block=%v err=%v", block, err)
	}
}

func TestProgressClosureAllowsDispatchAfterRelease(t *testing.T) {
	store := progresstest.New(t, "root-1")
	progresstest.SeedRows(t, store, "root-1",
		progress.RowSpec{Label: "step a", State: "done"},
		progress.RowSpec{Label: "step b"},
	)
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	mgr.Coordinator.ProgressClosure.Release("root-1", []string{"job-1"})

	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("after release must allow; block=%v err=%v", block, err)
	}
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("latch should clear after release")
	}
}

func TestProgressClosureRejectSilentWhenUnarmed(t *testing.T) {
	store := progresstest.New(t, "root-1")
	progresstest.SeedRows(t, store, "root-1", progress.RowSpec{Label: "step a"})
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("unarmed must allow; block=%v err=%v", block, err)
	}
}

func TestProgressClosureArmSkipsWhenNothingPending(t *testing.T) {
	store := progresstest.New(t, "root-1")
	seeded := progresstest.SeedRows(t, store, "root-1",
		progress.RowSpec{Label: "done"},
		progress.RowSpec{Label: "note", State: "optional"},
	)
	progresstest.UpdateRows(t, store, "root-1",
		progress.UpdateRowSpec{ID: seeded.Rows[0].ID, State: progresstest.State("done")},
	)

	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("arm must no-op when pending is zero")
	}
}

func TestProgressClosureRejectSkipsWorkerChild(t *testing.T) {
	store := progresstest.New(t, "root-1")
	progresstest.SeedRows(t, store, "root-1", progress.RowSpec{Label: "step a"})
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "child-1", ParentSessionID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("worker child must allow; block=%v err=%v", block, err)
	}
}

func TestProgressClosureRowEditsDoNotClearWithoutHandleAck(t *testing.T) {
	store := progresstest.New(t, "root-1")
	seeded := progresstest.SeedRows(t, store, "root-1",
		progress.RowSpec{Label: "step a"},
		progress.RowSpec{Label: "step b"},
	)
	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")

	progresstest.UpdateRows(t, store, "root-1",
		progress.UpdateRowSpec{ID: seeded.Rows[0].ID, State: progresstest.State("done")},
		progress.UpdateRowSpec{ID: seeded.Rows[1].ID, Label: progresstest.Label("step b — waiting on rerun")},
	)
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); !armed {
		t.Fatal("row edits consumed a worker finish without acknowledge_results")
	}
}
