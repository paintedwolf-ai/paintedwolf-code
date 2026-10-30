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
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newClosureGuardManager(t *testing.T, store progress.RunScopedStore) *Host {
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

// closureGuardPreInvoke evaluates the current reconciliation owner state.
func closureGuardPreInvoke(t *testing.T, mgr *Host, sess *api.Session, rootID, tool string) (bool, error) {
	t.Helper()
	content := mgr.RewindRuntime.Progress.Get(t.Context(), rootID)
	baseline, armed := mgr.Coordinator.ProgressClosure.Baseline(rootID)
	reject, blocked, err := mgr.ToolPolicy.Block(t.Context(), oar.AnchorCoordinatorPreInvoke, sess, tool, nil, func(gc *oar.GuardContext) error {
		guard.ObserveProgressItemNotClosedBeforeDispatch(sess, content, tool, baseline, armed, gc)
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
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] step a\n- [ ] step b\n- [ ] step c")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}

	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	// A blocked pre-invoke travels as a refusal carrying its code; skipRun stays
	// reserved for a hook that answered the call with a body.
	skip, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err == nil || skip {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok {
		t.Fatalf("err = %T want a refusal", err)
	}
	if refusal.Code() != "PROGRESS_ITEM_NOT_CLOSED" {
		t.Fatalf("refusal code = %q", refusal.Code())
	}
	if !strings.Contains(refusal.Body, "PROGRESS_ITEM_NOT_CLOSED") {
		t.Fatalf("body = %q", refusal.Body)
	}
	if !strings.Contains(refusal.Body, "job-1") {
		t.Fatalf("body = %q want the completed job it waits on", refusal.Body)
	}
}

// Every finish remains named until its exact set is acknowledged.
func TestProgressClosureAccumulatesCompletedWork(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a\n- [ ] step b")
	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-2")
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	baseline, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1")
	if !armed || len(baseline.Settled) != 2 || baseline.Settled[0] != "job-1" || baseline.Settled[1] != "job-2" {
		t.Fatalf("baseline = %+v armed=%v want job-1 then job-2 once each", baseline, armed)
	}
	if baseline.Content != "## Progress\n- [ ] step a\n- [ ] step b" {
		t.Fatalf("later completions must keep the first baseline: %q", baseline.Content)
	}
}

func TestProgressClosureRejectAllowsWriteWhenArmed(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	skip, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "write")
	if err == nil || skip {
		t.Fatalf("write should reject when armed; skip=%v err=%v", skip, err)
	}
}

func TestProgressClosureRejectAllowsReadWhenArmed(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "read")
	if err != nil || block {
		t.Fatalf("read must allow; block=%v err=%v", block, err)
	}
}

func TestProgressClosureAllowsDispatchAfterAcknowledgement(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] step a\n- [ ] step b")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	store.Set("root-1", "## Progress\n- [x] step a\n- [x] step b\n- [ ] step c")
	view := mgr.Coordinator.ProgressClosure.Reconciliation("root-1")
	if !mgr.Coordinator.ProgressClosure.Acknowledge("root-1", view.Token) {
		t.Fatal("current finishes were not acknowledged")
	}

	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("after acknowledgement must allow; block=%v err=%v", block, err)
	}
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("latch should clear after explicit acknowledgement")
	}
}

func TestProgressClosureAcknowledgesAlreadyRepresentedFinish(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] survey\n- [ ] challenge claims\n- [ ] report")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	view := mgr.Coordinator.ProgressClosure.Reconciliation("root-1")
	if !mgr.Coordinator.ProgressClosure.Acknowledge("root-1", view.Token) {
		t.Fatal("current worker finish was not acknowledged")
	}
	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("acknowledgement must allow; block=%v err=%v", block, err)
	}
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("latch should clear after explicit acknowledgement")
	}
}

func TestProgressClosureAcknowledgesFinishAfterDocumentCloses(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	store.Set("root-1", "## Progress\n- [x] step a\n- [>] note")
	view := mgr.Coordinator.ProgressClosure.Reconciliation("root-1")
	if !mgr.Coordinator.ProgressClosure.Acknowledge("root-1", view.Token) {
		t.Fatal("current finishes were not acknowledged")
	}

	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("acknowledged finish must allow; block=%v err=%v", block, err)
	}
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("latch should clear after explicit acknowledgement")
	}
}

func TestProgressClosureRejectSilentWhenUnarmed(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("unarmed must allow; block=%v err=%v", block, err)
	}
}

func TestProgressClosureArmSkipsWhenNothingPending(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] done\n- [>] note")
	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("arm must no-op when pending is zero")
	}
}

func TestProgressClosureRejectSkipsWorkerChild(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "child-1", ParentSessionID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "task")
	if err != nil || block {
		t.Fatalf("worker child must allow; block=%v err=%v", block, err)
	}
}

func TestProgressClosureAcknowledgesFinishAfterDescope(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a\n- [ ] step b\n- [ ] step c")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	store.Set("root-1", "## Progress\n- [~] step a\n- [ ] step b\n- [ ] step c")
	view := mgr.Coordinator.ProgressClosure.Reconciliation("root-1")
	if !mgr.Coordinator.ProgressClosure.Acknowledge("root-1", view.Token) {
		t.Fatal("current finishes were not acknowledged")
	}

	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "write")
	if err != nil || block {
		t.Fatalf("acknowledged descope must allow; block=%v err=%v", block, err)
	}
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("latch should clear after explicit acknowledgement")
	}
}

func TestProgressClosureReArmKeepsOriginalBaseline(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a\n- [ ] step b\n- [ ] step c")
	mgr := newClosureGuardManager(t, store)
	sess := &api.Session{ID: "root-1"}
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	store.Set("root-1", "## Progress\n- [x] step a\n- [ ] step b\n- [ ] step c")
	// Repeated finishes retain the original acknowledgement set.
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	view := mgr.Coordinator.ProgressClosure.Reconciliation("root-1")
	if !mgr.Coordinator.ProgressClosure.Acknowledge("root-1", view.Token) {
		t.Fatal("current finishes were not acknowledged")
	}

	block, err := closureGuardPreInvoke(t, mgr, sess, "root-1", "write")
	if err != nil || block {
		t.Fatalf("re-arm must keep original baseline; block=%v err=%v", block, err)
	}
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); armed {
		t.Fatal("latch should clear after explicit acknowledgement")
	}
}

func TestProgressClosureTerminalWriteDoesNotAcknowledgeFinish(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a\n- [ ] step b")
	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	store.Set("root-1", "## Progress\n- [x] step a\n- [ ] step b")
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); !armed {
		t.Fatal("document transition consumed a worker finish")
	}
}

func TestProgressClosureLabelWriteDoesNotClear(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] step a\n- [ ] step b")
	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	store.Set("root-1", "## Progress\n- [x] step a\n- [ ] step b — waiting on rerun")
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); !armed {
		t.Fatal("a label revision acknowledged a worker result")
	}
}

func TestProgressClosureUnchangedDoesNotClear(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [ ] step a\n- [ ] step b")
	mgr := newClosureGuardManager(t, store)
	mgr.Coordinator.ProgressClosure.Arm(t.Context(), "root-1", "job-1")
	// No acknowledgement was submitted.
	if _, armed := mgr.Coordinator.ProgressClosure.Baseline("root-1"); !armed {
		t.Fatal("unchanged checklist must leave the latch armed")
	}
}
