package toolexecution_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzcontext/authztest"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settings/settingstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type stubPolicy struct {
	decision *platform.PolicyDecision
}

func (s stubPolicy) Evaluate(context.Context, platform.PolicyContext) (*platform.PolicyDecision, error) {
	return s.decision, nil
}

func testApprovalStore(t *testing.T) *settings.ApprovalStore {
	t.Helper()
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	return store
}

func TestExecutorSilentAutoAllowEmitsNoAuthzEvent(t *testing.T) {
	ledgerStore := authztest.NewStore(t, "sess-auto")
	rec := authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: ledgerStore}}
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ok", nil
	}); err != nil {
		testutil.FailErr(t, "reg.Register failed", err)
	}
	exec := toolexecution.NewExecutor(stubPolicy{decision: &platform.PolicyDecision{Allowed: true}}, reg, "implement")
	exec.Approvals.SetAuthzRecorder(rec)
	_, err := exec.Invoke(context.Background(), "read", map[string]any{"path": "foo.go"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-auto",
			Agent: "implement"},
	})
	if err != nil {
		testutil.FailErr(t, "invoke", err)
	}
	rows, err := ledgerStore.ListEvents(t.Context(), "sess-auto")
	testutil.FailErr(t, "list events", err)
	if len(rows) != 0 {
		t.Fatalf("silent auto-allow emitted %d authorization events", len(rows))
	}
}

func TestExecutorPolicyBlockEmitsToolDenied(t *testing.T) {
	ledgerStore := authztest.NewStore(t, "sess-deny")
	rec := authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: ledgerStore}}
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ok", nil
	}); err != nil {
		testutil.FailErr(t, "reg.Register failed", err)
	}
	exec := toolexecution.NewExecutor(stubPolicy{decision: &platform.PolicyDecision{
		Blocked:     true,
		RejectCode:  "TOOL_PROFILE_DENIED",
		BlockReason: "TOOL_PROFILE_DENIED",
	}}, reg, "implement")
	exec.Approvals.SetAuthzRecorder(rec)
	_, err := exec.Invoke(context.Background(), "write", map[string]any{"path": "x.go", "content": "x"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-deny",
			Agent: "implement"},
	})
	if err == nil {
		t.Fatal("expected policy block")
	}
	rows, err := ledgerStore.ListEvents(context.Background(), "sess-deny")
	if err != nil {
		testutil.FailErr(t, "list events", err)
	}
	if len(rows) != 1 {
		t.Fatalf("policy deny emitted %d authorization events, want 1", len(rows))
	}
	if rows[0].Action != authzcontext.EventActionToolDenied {
		t.Fatalf("action = %q", rows[0].Action)
	}
	if rows[0].ResolvedBy != authzcontext.ResolvedBySystemDeny {
		t.Fatalf("resolved_by = %q", rows[0].ResolvedBy)
	}
}

func TestApprovalGateAutoAllow(t *testing.T) {
	store := testApprovalStore(t)
	gate := settings.NewRuleApprovalGate(store, settingstest.EmptySources())
	res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "read",
		},
		Scope: hitl.ActionScope{
			SessionID:  "sess-gate",
			ProjectDir: t.TempDir(),
		},
	})
	if err != nil {
		testutil.FailErr(t, "evaluate", err)
	}
	if !res.AutoApproved() {
		t.Fatalf("expected auto-approve at balanced, got %+v", res)
	}
}
