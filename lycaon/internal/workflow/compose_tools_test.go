package workflow_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
)

func TestComposeToolOmitsEffectiveYAML(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	policy, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	composer := &workflowcomposition.Composer{
		SessionStore: workflowdrafts.NewMemory(),
		Registry:     condReg,
		Agents:       agents,
		Policy:       policy,
	}
	if err := workflow.RegisterComposeTool(reg, composer); err != nil {
		testutil.FailErr(t, "workflow.RegisterComposeTool failed", err)
	}

	manifest := `id: slim-test-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	out, err := reg.Run(context.Background(), "workflow_compose", map[string]any{
		"manifest_yaml": manifest,
		"dry_run":       true,
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: "sess-1",
			Agent: orchestration.ProfileCoordinator},
	})
	testutil.FailErr(t, "reg.Run failed", err)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if _, ok := payload["effective_yaml"]; ok {
		t.Fatalf("tool JSON must omit effective_yaml: %s", out)
	}
	if _, ok := payload["effective_summary"]; !ok {
		t.Fatalf("missing effective_summary: %s", out)
	}
	if _, ok := payload["summary"]; !ok {
		t.Fatalf("missing summary: %s", out)
	}
	if !strings.Contains(out, "coordinator_brief") {
		t.Fatalf("expected coordinator_brief in effective_summary: %s", out)
	}
}
