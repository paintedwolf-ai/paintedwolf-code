package tools_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// settledJoinHITL answers the first card at once and refuses every join to it,
// the way the manager does once a decision has committed.
type settledJoinHITL struct {
	mu       sync.Mutex
	requests []string
	joins    atomic.Int32
}

func (m *settledJoinHITL) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "chk-settled-" + string(rune('a'+len(m.requests)))
	m.requests = append(m.requests, req.ToolCallID)
	return &hitl.CheckpointResponse{CheckpointID: id, Status: hitl.DecisionStatusPending}, nil
}

func (m *settledJoinHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{
		CheckpointID: checkpointID, Status: hitl.DecisionStatusApproved,
		Result: &hitl.DecisionResult{Approved: true},
	}, nil
}

func (m *settledJoinHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	m.joins.Add(1)
	return hitl.ErrCheckpointNotPending
}

func (m *settledJoinHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *settledJoinHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *settledJoinHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *settledJoinHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (m *settledJoinHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *settledJoinHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *settledJoinHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}

// A caller whose join lands after the card settled mints its own card instead
// of riding an answer that never held its action.
func TestExecutorSettledJoinReviewsItsOwnAction(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register write", reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		return "ok", nil
	}))

	mgr := &settledJoinHITL{}
	rt := approvalstate.NewToolApprovalCoalesce()
	policy := tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), gate)
	exec := tools.NewDefaultToolExecutor(policy, reg, "implement")
	exec.SetCheckpointManager(mgr, gate)
	exec.SetToolApprovalCoalesce(coalesceAdapter{rt: rt})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := map[string]any{"path": "a.txt", "content": "x"}
	tc := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    "chat-1",
		Agent:        "implement",
		ToolCallID:   "tc-first",
	}
	_, err = exec.Invoke(ctx, "write", args, tc)
	testutil.FailErr(t, "first invocation", err)
	// The first card's entry is still registered as pending: the terminal hook
	// that clears it has not run for this fake manager, so the second call joins.
	tc.ToolCallID = "tc-late"
	_, err = exec.Invoke(ctx, "write", args, tc)
	testutil.FailErr(t, "late invocation", err)
	mgr.mu.Lock()
	requests := append([]string(nil), mgr.requests...)
	mgr.mu.Unlock()
	if len(requests) != 2 || requests[1] != "tc-late" {
		t.Fatalf("RequestCheckpoint calls = %v, want the late caller to mint its own card", requests)
	}
	if mgr.joins.Load() != 1 {
		t.Fatalf("join attempts = %d want 1", mgr.joins.Load())
	}
}
