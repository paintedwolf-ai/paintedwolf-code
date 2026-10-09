package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
)

func TestComposeToolRequiresCoordinator(t *testing.T) {
	regConditions, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions", err)
	c := &workflowcomposition.Composer{SessionStore: workflowdrafts.NewMemory(), Registry: regConditions}
	reg := tools.NewDefaultRegistry()
	if err := RegisterComposeTool(reg, c); err != nil {
		testutil.FailErr(t, "RegisterComposeTool failed", err)
	}
	_, err = reg.Run(context.Background(), "workflow_compose", map[string]any{
		"manifest_yaml": "id: x\nversion: 1.0.0\nextends: plan@1.0.0\nphases:\n  - id: intake\n    next: build\n  - id: build\n    complete_when: delegation_closeout_complete\n",
	}, tools.ToolContext{Agent: "implementer", SessionID: "s"})
	if err == nil || !strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("err = %v", err)
	}
}
