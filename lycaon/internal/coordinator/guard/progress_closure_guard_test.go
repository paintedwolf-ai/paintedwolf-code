package guard

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveProgressReconciliationBeforeDispatch_blocksWhenArmed(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressReconciliationBeforeDispatch(sess, 2, "task", ProgressClosureBaseline{
		Unaccounted: []progress.UnaccountedResult{{Handle: "fixer#1"}},
	}, true, gc)
	if !hasRejectCode(t, gc, ProgressReconciliationRequiredCode) {
		t.Fatalf("want %s in %v", ProgressReconciliationRequiredCode, gc.Invocation.ArgValidationErrors)
	}
}

func TestObserveProgressReconciliationBeforeDispatch_allowsWhenUnarmed(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressReconciliationBeforeDispatch(sess, 1, "task", ProgressClosureBaseline{}, false, gc)
	if hasRejectCode(t, gc, ProgressReconciliationRequiredCode) {
		t.Fatal("unarmed latch must not observe reject")
	}
}

func TestObserveProgressReconciliationBeforeDispatch_allowsRead(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressReconciliationBeforeDispatch(sess, 1, "read", ProgressClosureBaseline{}, true, gc)
	if hasRejectCode(t, gc, ProgressReconciliationRequiredCode) {
		t.Fatal("read must not observe reject")
	}
}

func TestObserveProgressReconciliationBeforeDispatch_terminalDocumentStillNeedsAcknowledgement(t *testing.T) {
	sess := &api.Session{ID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressReconciliationBeforeDispatch(sess, 0, "task", ProgressClosureBaseline{}, true, gc)
	if !hasRejectCode(t, gc, ProgressReconciliationRequiredCode) {
		t.Fatal("document transition bypassed worker acknowledgement")
	}
}

func TestObserveProgressReconciliationBeforeDispatch_skipsWorkerChild(t *testing.T) {
	sess := &api.Session{ID: "child-1", ParentSessionID: "root-1"}
	gc := oar.NewGuardContext()
	ObserveProgressReconciliationBeforeDispatch(sess, 1, "task", ProgressClosureBaseline{}, true, gc)
	if hasRejectCode(t, gc, ProgressReconciliationRequiredCode) {
		t.Fatal("worker child must not observe reject")
	}
}

func hasRejectCode(t *testing.T, gc *oar.GuardContext, code string) bool {
	return oartest.EvaluateHasCode(t, gc, oar.AnchorCoordinatorPreInvoke, code) ||
		oartest.EvaluateHasCode(t, gc, oar.AnchorToolPreInvoke, code) ||
		oartest.EvaluateHasCode(t, gc, oar.AnchorCoordinatorCloseoutCheck, code)
}

func TestProgressReconciliationSuccessorSelectsFromCurrentFacts(t *testing.T) {
	pipeline := oartest.NewCoordinatorGuardPipeline(t)
	if pipeline == nil {
		t.Fatal("stock progress rules failed to load")
	}
	baseline := ProgressClosureBaseline{
		Settled:     []string{"worker"},
		Unaccounted: []progress.UnaccountedResult{{Handle: "worker#1"}},
	}
	for _, tc := range []struct {
		name                  string
		openSteps             int
		tool                  string
		armed, child, blocked bool
	}{
		{"open checklist", 1, "task", true, false, true},
		{"terminal document", 0, "task", true, false, true},
		{"unarmed", 1, "task", false, false, false},
		{"unarmed zero", 0, "task", false, false, false},
		{"read only", 1, "read", true, false, false},
		{"worker child", 1, "task", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &api.Session{ID: "session"}
			if tc.child {
				session.ParentSessionID = "root"
			}
			facts := oar.NewGuardContext()
			ObserveProgressReconciliationBeforeDispatch(session, tc.openSteps, tc.tool, baseline, tc.armed, facts)
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
		})
	}
}
