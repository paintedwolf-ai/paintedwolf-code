package toolexecution

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestDirectCommandOperationRejectsBeforeArgumentValidation(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register operation", registry.RegisterOperation("edit"))
	executor := NewExecutor(nil, registry, "implement")
	reject := executor.validateInvocation(t.Context(), "edit", "implement", map[string]any{"unexpected": true}, &tools.ToolContext{})
	if reject == nil || reject.Code != "TOOL_COMMAND_OPERATION" || reject.Data["need"] != "edit" {
		t.Fatalf("direct operation rejection = %+v", reject)
	}
}
