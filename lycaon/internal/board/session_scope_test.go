package board

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestCoordinatorBoardSessionID(t *testing.T) {
	if got := CoordinatorBoardSessionID(tools.ToolContext{SessionID: "coord"}); got != "coord" {
		t.Fatalf("coordinator = %q", got)
	}
	if got := CoordinatorBoardSessionID(tools.ToolContext{
		SessionID:        "child",
		HandoffSessionID: "coord",
	}); got != "coord" {
		t.Fatalf("worker child = %q, want coord", got)
	}
}
