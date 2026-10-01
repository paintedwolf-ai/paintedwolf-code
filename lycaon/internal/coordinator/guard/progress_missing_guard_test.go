package guard

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveProgressMissingBeforeDispatch_blocksTaskWithoutPlan(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressMissingBeforeDispatch(sess, "", "task", false, gc)
	if !hasRejectCode(gc, ProgressMissingCode) {
		t.Fatalf("want %s in %v", ProgressMissingCode, gc.ArgValidationErrors)
	}
}

func TestObserveProgressMissingBeforeDispatch_blocksWriteWithoutChecklist(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	for _, tool := range []string{"write", "edit", "replace_lines"} {
		gc := oar.NewGuardContext()
		ObserveProgressMissingBeforeDispatch(sess, "", tool, false, gc)
		if !hasRejectCode(gc, ProgressMissingCode) {
			t.Fatalf("%s: want %s in %v", tool, ProgressMissingCode, gc.ArgValidationErrors)
		}
	}
}

func TestObserveProgressMissingBeforeDispatch_allowsWithPlan(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressMissingBeforeDispatch(sess, "- [ ] Research libraries", "task", false, gc)
	if hasRejectCode(gc, ProgressMissingCode) {
		t.Fatal("checklist present must not observe reject")
	}
}

func TestObserveProgressMissingBeforeDispatch_allowsWriteWithChecklist(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressMissingBeforeDispatch(sess, "- [ ] Ship feature", "write", false, gc)
	if hasRejectCode(gc, ProgressMissingCode) {
		t.Fatal("checklist present must not observe reject")
	}
}

func TestObserveProgressMissingBeforeDispatch_allowsWebSearchWithoutPlan(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressMissingBeforeDispatch(sess, "", "web_search", false, gc)
	if hasRejectCode(gc, ProgressMissingCode) {
		t.Fatal("web_search must not observe reject")
	}
}

func TestObserveProgressMissingBeforeDispatch_skipsWorkerChild(t *testing.T) {
	sess := &api.Session{ID: "child-1", ParentSessionID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressMissingBeforeDispatch(sess, "", "task", false, gc)
	if hasRejectCode(gc, ProgressMissingCode) {
		t.Fatal("worker child must not observe reject")
	}
}

func TestObserveProgressMissingBeforeDispatch_allowsOnReviewLoop(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressMissingBeforeDispatch(sess, "", "task", true, gc)
	if hasRejectCode(gc, ProgressMissingCode) {
		t.Fatal("review loop must not observe reject")
	}
}
