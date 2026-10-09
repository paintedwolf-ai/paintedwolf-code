package runstate_test

import (
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
)

func TestSatisfyGateInVarsHumanApproval(t *testing.T) {
	vars := runstate.SatisfyGateInVars(nil, "human_approval")
	if !conditions.DotPathTruthy(vars, "human_approval.issued") {
		t.Fatal("expected human_approval.issued")
	}
}
