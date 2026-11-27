package statetools

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/lifecycle"
	"github.com/lycaon/lycaon/pkg/api"
)

type startSessions struct{ root string }

func (s startSessions) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{WorkspacePath: s.root}, nil
}

func registerStart(t *testing.T, root string) *tools.DefaultRegistry {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	if err := RegisterStateTools(reg, StateToolDeps{Starts: &lifecycle.Admission{}, Sessions: startSessions{root}}); err != nil {
		t.Fatalf("RegisterStateTools failed: %v", err)
	}
	return reg
}

func TestStateStartRefusesIncompleteWorkflowIdentity(t *testing.T) {
	root := t.TempDir()
	reg := registerStart(t, root)
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}}
	for _, args := range []map[string]any{nil, {"workflow_id": "requested"}, {"workflow_version": "1.0.0"}} {
		if out, err := reg.Run(t.Context(), "state_start", args, tctx); err == nil || out != "" {
			t.Fatalf("state_start accepted %v output=%q", args, out)
		}
	}
}

func TestStateStartRefusesUnboundInvocation(t *testing.T) {
	reg := registerStart(t, t.TempDir())
	if out, err := reg.Run(t.Context(), "state_start", map[string]any{"workflow_id": "requested", "workflow_version": "1.0.0"}, tools.ToolContext{}); err == nil || out != "" {
		t.Fatalf("state_start accepted unbound invocation output=%q err=%v", out, err)
	}
}

func TestStateToolsRequireWorkflowAdmission(t *testing.T) {
	if err := RegisterStateTools(tools.NewDefaultRegistry(), StateToolDeps{}); err == nil {
		t.Fatal("registered state_start without workflow admission")
	}
}
