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
	if !hasRejectCode(gc, ProgressItemNotClosedCode) {
		t.Fatalf("want %s in %v", ProgressItemNotClosedCode, gc.Invocation.ArgValidationErrors)
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_allowsWhenUnarmed(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	content := "## Progress\n- [ ] ship\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Content: content}, false, gc)
	if hasRejectCode(gc, ProgressItemNotClosedCode) {
		t.Fatal("unarmed latch must not observe reject")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_allowsRead(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	content := "## Progress\n- [ ] ship\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "read", ProgressClosureBaseline{Content: content}, true, gc)
	if hasRejectCode(gc, ProgressItemNotClosedCode) {
		t.Fatal("read must not observe reject")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_terminalDocumentStillNeedsAcknowledgement(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	armContent := "## Progress\n- [ ] ship\n- [ ] verify\n"
	content := "## Progress\n- [x] ship\n- [ ] verify\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Closed: 0, Content: armContent}, true, gc)
	if !hasRejectCode(gc, ProgressItemNotClosedCode) {
		t.Fatal("document transition bypassed worker acknowledgement")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_relabelDoesNotAcknowledgeWorker(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	baseline := "- [x] survey\n- [ ] challenge claims\n- [ ] report"
	current := "- [x] survey with more detail\n- [ ] challenge claims\n- [ ] report"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, current, "task", ProgressClosureBaseline{Closed: 1, Content: baseline}, true, gc)
	if !hasRejectCode(gc, ProgressItemNotClosedCode) {
		t.Fatal("label rewrite acknowledged a worker result")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_blocksOnIdenticalChecklist(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	content := "## Progress\n- [x] survey\n- [ ] challenge claims\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Closed: 1, Content: content}, true, gc)
	if !hasRejectCode(gc, ProgressItemNotClosedCode) {
		t.Fatal("identical checklist since arm must observe reject")
	}
}

func TestObserveProgressItemNotClosedBeforeDispatch_skipsWorkerChild(t *testing.T) {
	sess := &api.Session{ID: "child-1", ParentSessionID: "root-1"}
	content := "## Progress\n- [ ] ship\n"
	gc := oar.NewGuardContext()
	ObserveProgressItemNotClosedBeforeDispatch(sess, content, "task", ProgressClosureBaseline{Content: content}, true, gc)
	if hasRejectCode(gc, ProgressItemNotClosedCode) {
		t.Fatal("worker child must not observe reject")
	}
}

func hasRejectCode(gc *oar.GuardContext, code string) bool {
	return EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, code) ||
		EvaluateObserveHasCode(gc, oar.AnchorToolPreInvoke, code) ||
		EvaluateObserveHasCode(gc, oar.AnchorCoordinatorCloseoutCheck, code)
}
