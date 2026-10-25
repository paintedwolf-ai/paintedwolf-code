package toolexecution_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestStateAncestorWriteGrantRefusesBeforeApproval(t *testing.T) {
	ancestor := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(ancestor, "state"))
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	invoked := false
	testutil.FailErr(t, "register command owner", registry.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		invoked = true
		return "", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), registry, "implement")
	manager := &asyncHITL{requested: make(chan struct{}, 1)}
	executor.Approvals.SetCheckpointManager(t.Context(), manager, gate)
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "root"}, Identity: tools.InvocationIdentity{SessionID: "state-root", ToolCallID: "grant-state-parent", Agent: "implement"}}
	_, err = executor.Invoke(t.Context(), "command", map[string]any{"command": "echo ok", "capability_request": map[string]any{"write_root": ancestor}}, tc)
	var rejection *toolrejection.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != isolation.CodeControlPlaneDenied {
		t.Fatalf("lost state root floor: %v", err)
	}
	if invoked {
		t.Fatal("refused grant invoked owner")
	}
	select {
	case <-manager.requested:
		t.Fatal("state ancestor grant asked for approval")
	default:
	}
}
