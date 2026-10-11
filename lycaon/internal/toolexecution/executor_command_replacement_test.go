package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
)

func TestExactReplacementRequiresCallableNativeHandler(t *testing.T) {
	executor := NewExecutor(nil, nil, "")
	if reject := executor.exactCommandReplacementReject(
		context.Background(), "command", "implement", map[string]any{"command": "sleep 2"}, tools.ToolContext{},
	); reject != nil {
		t.Fatalf("unregistered replacement blocked command: %+v", reject)
	}
}
