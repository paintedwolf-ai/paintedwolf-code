package workflow

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestDepthFanOutCount(t *testing.T) {
	if DepthFanOutCount(workflowdef.DepthNone) != 0 {
		t.Fatal("none fan-out")
	}
	if DepthFanOutCount(workflowdef.DepthLight) != 1 {
		t.Fatal("light fan-out")
	}
	if DepthFanOutCount(workflowdef.DepthThorough) != 3 {
		t.Fatal("thorough fan-out")
	}
}
