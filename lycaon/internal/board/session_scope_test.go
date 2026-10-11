package board

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestCoordinatorBoardSessionID(t *testing.T) {
	if got := CoordinatorBoardSessionID(tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "coord"},
	}); got != "coord" {
		t.Fatalf("coordinator = %q", got)
	}
	if got := CoordinatorBoardSessionID(tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child",
			HandoffSessionID: "coord"},
	}); got != "coord" {
		t.Fatalf("worker child = %q, want coord", got)
	}
}
