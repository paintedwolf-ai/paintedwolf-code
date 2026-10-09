package runstate_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func TestSatisfyGateInVarsHumanApproval(t *testing.T) {
	vars := runstate.SatisfyGateInVars(nil, "human_approval")
	if !conditions.DotPathTruthy(vars, "human_approval.issued") {
		t.Fatal("expected human_approval.issued")
	}
}
