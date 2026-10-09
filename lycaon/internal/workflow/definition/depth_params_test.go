package definition_test

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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
