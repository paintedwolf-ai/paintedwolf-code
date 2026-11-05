package guard

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveProgressItemNotClosedBeforeDispatch_blocksWhenArmed(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	content := "## Progress\n- [ ] ship\n- [ ] verify\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Closed: 0, Content: content}, true, gc)
	if !hasRejectCode(gc, ProgressReconciliationRequiredCode) {
		t.Fatalf("want %s in %v", ProgressReconciliationRequiredCode, gc.Invocation.ArgValidationErrors)
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_allowsWhenUnarmed(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	content := "## Progress\n- [ ] ship\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Content: content}, false, gc)
	if hasRejectCode(gc, ProgressReconciliationRequiredCode) {
		t.Fatal("unarmed latch must not observe reject")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_allowsRead(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	content := "## Progress\n- [ ] ship\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "read", ProgressClosureBaseline{Content: content}, true, gc)
	if hasRejectCode(gc, ProgressReconciliationRequiredCode) {
		t.Fatal("read must not observe reject")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_terminalDocumentStillNeedsAcknowledgement(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	armContent := "## Progress\n- [ ] ship\n- [ ] verify\n"
	content := "## Progress\n- [x] ship\n- [ ] verify\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Closed: 0, Content: armContent}, true, gc)
	if !hasRejectCode(gc, ProgressReconciliationRequiredCode) {
		t.Fatal("document transition bypassed worker acknowledgement")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_relabelDoesNotAcknowledgeWorker(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	baseline := "- [x] survey\n- [ ] challenge claims\n- [ ] report"
	current := "- [x] survey with more detail\n- [ ] challenge claims\n- [ ] report"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, current, "task", ProgressClosureBaseline{Closed: 1, Content: baseline}, true, gc)
	if !hasRejectCode(gc, ProgressReconciliationRequiredCode) {
		t.Fatal("label rewrite acknowledged a worker result")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_blocksOnIdenticalChecklist(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	content := "## Progress\n- [x] survey\n- [ ] challenge claims\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Closed: 1, Content: content}, true, gc)
	if !hasRejectCode(gc, ProgressReconciliationRequiredCode) {
		t.Fatal("identical checklist since arm must observe reject")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_skipsWorkerChild(t *testing.T) {
	sess := &api.Session{ID: "child-1", ParentSessionID: "root-1"}
	content := "## Progress\n- [ ] ship\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Content: content}, true, gc)
	if hasRejectCode(gc, ProgressReconciliationRequiredCode) {
		t.Fatal("worker child must not observe reject")
	}
}

func hasRejectCode(gc *oar.GuardContext, code string) bool {
	return EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, code) ||
		EvaluateObserveHasCode(gc, oar.AnchorToolPreInvoke, code) ||
		EvaluateObserveHasCode(gc, oar.AnchorCoordinatorCloseoutCheck, code)
}

func TestProgressReconciliationSuccessorSelectsFromCurrentFacts(t *testing.T) {
	pipeline := hostFinishOARPipeline()
	if pipeline == nil {
		t.Fatal("stock progress rules failed to load")
	}
	baseline := ProgressClosureBaseline{Content: "- [ ] Deliver", Settled: []string{"worker"}}
	for _, tc := range []struct {
		name, content, tool   string
		armed, child, blocked bool
	}{
		{"old blocking predicate", baseline.Content, "task", true, false, true},
		{"terminal document", "- [x] Deliver", "task", true, false, true},
		{"relabelled document", "- [ ] Deliver revised", "task", true, false, true},
		{"empty document", "", "task", true, false, true},
		{"unarmed", "- [ ] Deliver", "task", false, false, false},
		{"unarmed empty", "", "task", false, false, false},
		{"read only", baseline.Content, "read", true, false, false},
		{"worker child", baseline.Content, "task", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &api.Session{ID: "session"}
			if tc.child {
				session.ParentSessionID = "root"
			}
			facts := oar.NewGuardContext()
			ObserveProgressItemNotClosedBeforeDispatch(session, tc.content, tc.tool, baseline, tc.armed, facts)
			result, err := pipeline.EvaluateBlock(t.Context(), oar.AnchorCoordinatorPreInvoke, facts)
			if err != nil {
				t.Fatalf("stock progress selection failed: %v", err)
			}
			selected := ""
			if result != nil && result.Decision != nil {
				selected = result.Decision.Code
			}
			if tc.blocked && selected != ProgressReconciliationRequiredCode || !tc.blocked && selected != "" {
				t.Fatalf("selected=%q blocked=%v", selected, tc.blocked)
			}
			if resultHasCode(result, "PROGRESS_ITEM_NOT_CLOSED") {
				t.Fatal("deprecated checklist-change rule resurfaced")
			}
		})
	}
}
