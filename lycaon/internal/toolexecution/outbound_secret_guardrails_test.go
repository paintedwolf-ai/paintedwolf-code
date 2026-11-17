package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// guardrailsHITL records each raised card in the shared event order and
// rejects it.
type guardrailsHITL struct {
	mu     sync.Mutex
	events *[]string
	reqs   []hitl.CheckpointRequest
}

func (m *guardrailsHITL) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	*m.events = append(*m.events, "card")
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	return &hitl.CheckpointResponse{CheckpointID: "chk-secret", Status: hitl.DecisionStatusPending}, nil
}

func (m *guardrailsHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{
		CheckpointID: checkpointID,
		Status:       hitl.DecisionStatusRejected,
		Result:       &hitl.DecisionResult{Approved: false},
	}, nil
}

func (m *guardrailsHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}

func (m *guardrailsHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (m *guardrailsHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (m *guardrailsHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}

func (m *guardrailsHITL) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

func (m *guardrailsHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}

func (m *guardrailsHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func (m *guardrailsHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}

func (m *guardrailsHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

func guardrailsExecutor(t *testing.T) (*Executor, *guardrailsHITL, *[]string) {
	t.Helper()
	var events []string
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register command", reg.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		events = append(events, "run")
		return "ok", nil
	}))
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	mgr := &guardrailsHITL{events: &events}
	exec := NewExecutor(nil, reg, "implement")
	exec.Secrets.SetSecretMatcher(m)
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, nil)
	return exec, mgr, &events
}

// A credential in command argv is screened before the tool runs: the gate
// asks through the outbound-secret gate, and a declined card never runs the
// tool nor carries the value. Clean argv runs without a card.
func TestOutboundSecretArgvAsksBeforeRun(t *testing.T) {
	tc := tools.ToolContext{
		Invocation: tools.Invocation{Contract: catalogContract(t, "command")},
		Identity:   tools.InvocationIdentity{SessionID: "s1", ToolCallID: "tc1", ProjectID: "project-id", Agent: "implement"},
	}

	clean, _, cleanEvents := guardrailsExecutor(t)
	_, err := clean.Invoke(t.Context(), "command", map[string]any{"command": "curl https://example.test"}, tc)
	testutil.FailErr(t, "clean command", err)
	if strings.Join(*cleanEvents, ",") != "run" {
		t.Fatalf("clean argv events = %v, want run only", *cleanEvents)
	}

	exec, mgr, events := guardrailsExecutor(t)
	_, err = exec.Invoke(t.Context(), "command", argvWithCredential(), tc)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != toolrejection.OutboundSecretDeniedCode {
		t.Fatalf("declined secret send err = %v, want %s", err, toolrejection.OutboundSecretDeniedCode)
	}
	if strings.Join(*events, ",") != "card" {
		t.Fatalf("secret argv events = %v, want one card and no run", *events)
	}
	req := mgr.reqs[0]
	if req.Decision == nil || req.Decision.Primary != api.GateSecretOutbound {
		t.Fatalf("card decision = %+v, want primary %s", req.Decision, api.GateSecretOutbound)
	}
	raw, err := json.Marshal(req)
	testutil.FailErr(t, "marshal card", err)
	if strings.Contains(string(raw)+err2s(reject), plantAWS) {
		t.Fatal("card or refusal carries the matched value")
	}
}

func err2s(reject *toolrejection.ToolReject) string {
	raw, _ := json.Marshal(reject.Data)
	return reject.Observation + string(raw)
}
