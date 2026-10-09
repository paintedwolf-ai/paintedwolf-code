package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
)

func TestPersistToolRejectsConfirmFalse(t *testing.T) {
	p, store := testPersister(t)
	reg := tools.NewDefaultRegistry()
	if err := RegisterPersistTool(reg, p); err != nil {
		testutil.FailErr(t, "RegisterPersistTool failed", err)
	}
	manifest := `id: hotfix
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
	if err := store.Upsert(context.Background(), "sess-1", []byte(manifest), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	_, err := reg.Run(context.Background(), "workflow_persist", map[string]any{
		"workflow_id": "hotfix",
		"version":     "1.0.0",
		"confirm":     false,
	}, toolContext(orchestration.ProfileCoordinator, "sess-1", t.TempDir()))
	var notConfirmed *workflowcomposition.PersistNotConfirmedError
	if !errors.As(err, &notConfirmed) {
		t.Fatalf("err = %v", err)
	}
}

func toolContext(agent, sessionID, dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: agent,
			SessionID: sessionID},
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: "r1"},
	}
}
