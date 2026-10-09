package toolexecution_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/settings"
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
	mem := authzcontext.NewMemoryStore()
	rec := authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: mem}}
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}
	exec := toolexecution.NewExecutor(stubPolicy{decision: &platform.PolicyDecision{Allowed: true}}, reg, "implement")
	exec.Approvals.SetAuthzRecorder(rec)
	_, err := exec.Invoke(context.Background(), "read", map[string]any{"path": "foo.go"}, tools.ToolContext{
		SessionID: "sess-auto",
		Agent:     "implement",
	})
	if err != nil {
		testutil.FailErr(t, "invoke", err)
	}
	if mem.EventCount("sess-auto") != 0 {
		t.Fatalf("silent auto-allow must emit no authz event, got %d", mem.EventCount("sess-auto"))
	}
}

func TestExecutorPolicyBlockEmitsToolDenied(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	rec := authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: mem}}
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}
	exec := toolexecution.NewExecutor(stubPolicy{decision: &platform.PolicyDecision{
		Blocked:     true,
		RejectCode:  "TOOL_PROFILE_DENIED",
		BlockReason: "TOOL_PROFILE_DENIED",
	}}, reg, "implement")
	exec.Approvals.SetAuthzRecorder(rec)
	_, err := exec.Invoke(context.Background(), "write", map[string]any{"path": "x.go", "content": "x"}, tools.ToolContext{
		SessionID: "sess-deny",
		Agent:     "implement",
	})
	if err == nil {
		t.Fatal("expected policy block")
	}
	if mem.EventCount("sess-deny") != 1 {
		t.Fatalf("policy deny must emit one tool_denied event, got %d", mem.EventCount("sess-deny"))
	}
	rows, err := mem.ListEvents(context.Background(), "sess-deny")
	if err != nil {
		testutil.FailErr(t, "list events", err)
	}
	if rows[0].Action != authzcontext.EventActionToolDenied {
		t.Fatalf("action = %q", rows[0].Action)
	}
	if rows[0].ResolvedBy != authzcontext.ResolvedBySystemDeny {
		t.Fatalf("resolved_by = %q", rows[0].ResolvedBy)
	}
}

func TestApprovalGateAutoAllowNoEvent(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	_ = authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: mem}}
	store := testApprovalStore(t)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
		Tool: "read", SessionID: "sess-gate", ProjectDir: t.TempDir(),
	})
	if err != nil {
		testutil.FailErr(t, "evaluate", err)
	}
	if !res.AutoApproved() {
		t.Fatalf("expected auto-approve at balanced, got %+v", res)
	}
	if mem.EventCount("sess-gate") != 0 {
		t.Fatal("gate auto-approve must not append authz events")
	}
}
