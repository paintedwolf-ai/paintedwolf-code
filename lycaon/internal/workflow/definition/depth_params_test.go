package definition_test

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"testing"
)

func TestDepthFanOutCount(t *testing.T) {
	if workflowdef.DepthFanOutCount(workflowdef.DepthNone) != 0 {
		t.Fatal("none fan-out")
	}
	if workflowdef.DepthFanOutCount(workflowdef.DepthLight) != 1 {
		t.Fatal("light fan-out")
	}
	if workflowdef.DepthFanOutCount(workflowdef.DepthThorough) != 3 {
		t.Fatal("thorough fan-out")
	}
}
