package toolexecution_test

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type coalesceHITL struct {
	mu           sync.Mutex
	requests     int
	checkpointID string
	approved     atomic.Bool
	joinedPatch  atomic.Int32
	req          chan struct{}
}

func (m *coalesceHITL) RequestCheckpoint(_ context.Context, _ hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	if m.checkpointID == "" {
		m.checkpointID = "chk-coalesce-1"
	}
	select {
	case m.req <- struct{}{}:
	default:
	}
	return &hitl.CheckpointResponse{CheckpointID: m.checkpointID, Status: hitl.DecisionStatusPending}, nil
}

func (m *coalesceHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	if m.approved.Load() {
		return &hitl.CheckpointResponse{
			CheckpointID: checkpointID,
			Status:       hitl.DecisionStatusApproved,
			Result:       &hitl.DecisionResult{Approved: true},
		}, nil
	}
	return &hitl.CheckpointResponse{CheckpointID: checkpointID, Status: hitl.DecisionStatusPending}, nil
}

func (m *coalesceHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *coalesceHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *coalesceHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (m *coalesceHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *coalesceHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *coalesceHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (m *coalesceHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	m.joinedPatch.Add(1)
	return nil
}

func (m *coalesceHITL) approve() { m.approved.Store(true) }

// waitJoinedPatch blocks until the joiner's joined_count patch lands — the
// join is asynchronous relative to the mint.
func waitJoinedPatch(t *testing.T, ctx context.Context, mgr *coalesceHITL) {
	t.Helper()
	for mgr.joinedPatch.Load() == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for joined_count patch")
		case <-time.After(time.Millisecond):
		}
	}
}

type countingRationale struct {
	n atomic.Int32
}

func (c *countingRationale) Enabled() bool { return true }
func (c *countingRationale) AttachAsync(context.Context, toolapproval.AIRationaleAttachRequest) {
	c.n.Add(1)
}

type failOnceHITL struct {
	mu       sync.Mutex
	attempts int
	approved atomic.Bool
}

func (m *failOnceHITL) RequestCheckpoint(context.Context, hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.attempts == 1 {
		return nil, fmt.Errorf("mint failed")
	}
	return &hitl.CheckpointResponse{CheckpointID: "chk-after-abort", Status: hitl.DecisionStatusPending}, nil
}

func (m *failOnceHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	if m.approved.Load() {
		return &hitl.CheckpointResponse{
			CheckpointID: checkpointID,
			Status:       hitl.DecisionStatusApproved,
			Result:       &hitl.DecisionResult{Approved: true},
		}, nil
	}
	return &hitl.CheckpointResponse{CheckpointID: checkpointID, Status: hitl.DecisionStatusPending}, nil
}

func (m *failOnceHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *failOnceHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *failOnceHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (m *failOnceHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *failOnceHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *failOnceHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (m *failOnceHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

type coalesceAdapter struct {
	rt *approvalstate.ToolApprovalCoalesce
}

func (a coalesceAdapter) Begin(chat, key string) (toolapproval.ToolApprovalCoalesceBegin, string) {
	b, id := a.rt.Begin(chat, key)
	return toolapproval.ToolApprovalCoalesceBegin(b), id
}
func (a coalesceAdapter) RegisterPending(chat, key, id, tc string) {
	a.rt.RegisterPending(chat, key, id, tc)
}
func (a coalesceAdapter) AbortMint(chat, key string) { a.rt.AbortMint(chat, key) }
func (a coalesceAdapter) ClearPending(chat, key string) {
	a.rt.ClearPending(chat, key)
}
func (a coalesceAdapter) RecordDeny(chat, key string) { a.rt.RecordDeny(chat, key) }
func (a coalesceAdapter) ClearDeny(chat, key string)  { a.rt.ClearDeny(chat, key) }
func (a coalesceAdapter) NoteJoin(chat, key, tc string) int {
	return a.rt.NoteJoin(chat, key, tc)
}
func (a coalesceAdapter) JoinedCount(chat, key string) int { return a.rt.JoinedCount(chat, key) }
func (a coalesceAdapter) JoinedToolCallIDs(chat, key string) []string {
	return a.rt.JoinedToolCallIDs(chat, key)
}

func TestExecutorCoalesceIdenticalPendingSharesCheckpoint(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}

	mgr := &coalesceHITL{req: make(chan struct{}, 2)}
	rt := approvalstate.NewToolApprovalCoalesce()
	rationale := &countingRationale{}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(mgr, gate)
	exec.Approvals.SetToolApprovalCoalesce(coalesceAdapter{rt: rt})
	exec.Approvals.SetAIRationaleAttacher(rationale)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := map[string]any{"path": "a.txt", "content": "x"}
	tcBase := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    "chat-1",
		Agent:        "implement",
	}

	type result struct{ err error }
	ch := make(chan result, 2)
	go func() {
		tc := tcBase
		tc.ToolCallID = "tc-a"
		_, err := exec.Invoke(ctx, "write", args, tc)
		ch <- result{err}
	}()
	go func() {
		tc := tcBase
		tc.ToolCallID = "tc-b"
		_, err := exec.Invoke(ctx, "write", args, tc)
		ch <- result{err}
	}()

	select {
	case <-mgr.req:
	case <-ctx.Done():
		t.Fatal("timed out waiting for mint")
	}
	// Approve only after the joiner shares the open card; counts are asserted
	// once both waiters return and can no longer change.
	waitJoinedPatch(t, ctx, mgr)
	mgr.approve()
	for i := 0; i < 2; i++ {
		res := <-ch
		if res.err != nil {
			t.Fatalf("waiter %d err = %v", i, res.err)
		}
	}

	mgr.mu.Lock()
	reqs := mgr.requests
	mgr.mu.Unlock()
	if reqs != 1 {
		t.Fatalf("RequestCheckpoint calls = %d want 1", reqs)
	}
	if rationale.n.Load() != 1 {
		t.Fatalf("AttachAsync calls = %d want 1", rationale.n.Load())
	}
}

func TestExecutorCoalesceDifferentArgsMintTwo(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}

	mgr := &coalesceHITL{req: make(chan struct{}, 4)}
	rt := approvalstate.NewToolApprovalCoalesce()
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(mgr, gate)
	exec.Approvals.SetToolApprovalCoalesce(coalesceAdapter{rt: rt})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tcBase := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    "chat-1",
		Agent:        "implement",
	}

	done := make(chan struct{}, 2)
	go func() {
		tc := tcBase
		tc.ToolCallID = "tc-1"
		_, _ = exec.Invoke(ctx, "write", map[string]any{"path": "a.txt", "content": "x"}, tc)
		done <- struct{}{}
	}()
	go func() {
		tc := tcBase
		tc.ToolCallID = "tc-2"
		_, _ = exec.Invoke(ctx, "write", map[string]any{"path": "b.txt", "content": "x"}, tc)
		done <- struct{}{}
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-mgr.req:
		case <-ctx.Done():
			t.Fatal("timed out waiting for mints")
		}
	}
	mgr.mu.Lock()
	reqs := mgr.requests
	mgr.mu.Unlock()
	if reqs != 2 {
		t.Fatalf("RequestCheckpoint calls = %d want 2", reqs)
	}
	mgr.approve()
	<-done
	<-done
}

func TestExecutorCoalesceAbortMintAllowsRetry(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}

	mgr := &failOnceHITL{}
	rt := approvalstate.NewToolApprovalCoalesce()
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(mgr, gate)
	exec.Approvals.SetToolApprovalCoalesce(coalesceAdapter{rt: rt})

	tc := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    "chat-1",
		ToolCallID:   "tc-abort",
		Agent:        "implement",
	}
	args := map[string]any{"path": "a.txt", "content": "x"}
	_, err = exec.Invoke(context.Background(), "write", args, tc)
	if err == nil {
		t.Fatal("expected mint failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		tc2 := tc
		tc2.ToolCallID = "tc-retry"
		_, err := exec.Invoke(ctx, "write", args, tc2)
		done <- err
	}()
	mgr.approved.Store(true)
	if err := <-done; err != nil {
		t.Fatalf("retry after AbortMint: %v", err)
	}
	mgr.mu.Lock()
	attempts := mgr.attempts
	mgr.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("attempts = %d want 2", attempts)
	}
}

func TestExecutorNetworkCoalesceCrossWorkerRootSession(t *testing.T) {
	mgr := &coalesceHITL{req: make(chan struct{}, 2)}
	rt := approvalstate.NewToolApprovalCoalesce()
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(mgr, nil)
	exec.Approvals.SetToolApprovalCoalesce(coalesceAdapter{rt: rt})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type out struct{ ok bool }
	ch := make(chan out, 2)
	go func() {
		ok := exec.ResolveEgressForTest(ctx, confine.EgressCommand{
			SessionID: "worker-a", RootSessionID: "coord-1", ToolCallID: "tc-a",
		}, "api.example.com")
		ch <- out{ok}
	}()
	go func() {
		ok := exec.ResolveEgressForTest(ctx, confine.EgressCommand{
			SessionID: "worker-b", RootSessionID: "coord-1", ToolCallID: "tc-b",
		}, "api.example.com")
		ch <- out{ok}
	}()

	select {
	case <-mgr.req:
	case <-ctx.Done():
		t.Fatal("timed out waiting for network mint")
	}
	waitJoinedPatch(t, ctx, mgr)
	mgr.approve()
	mgr.mu.Lock()
	reqs := mgr.requests
	mgr.mu.Unlock()
	if reqs != 1 {
		t.Fatalf("network RequestCheckpoint calls = %d want 1 (cross-worker RootSessionID)", reqs)
	}
	for i := 0; i < 2; i++ {
		if !(<-ch).ok {
			t.Fatalf("waiter %d denied", i)
		}
	}
}

func TestExactLeaseSilencesDetection(t *testing.T) {
	tmp := t.TempDir()
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	approvals := settings.NewRuleApprovalGate(store, settings.Sources{Detections: func() settings.DetectionSource {
		return stubDetSource{
			ok: true,
			match: hitl.DetectionMatch{
				PackID: "p", RuleID: "r", RuleTitle: "t", Level: "critical",
				External: true, Unrecoverable: true, Tagged: true,
			},
		}
	}})
	action := hitl.ProposedAction{
		Tool:       "command",
		Args:       map[string]any{"command": "aws s3 rm --recursive s3://x"},
		ProjectDir: tmp,
		SessionID:  "chat",
		Contained:  hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{tmp}},
	}
	offer := hitl.ExactActionSetOffer(action, []string{hitl.GrantKey(action)})
	_, err = approvals.ApplyGrant(offer.Grant)
	testutil.FailErr(t, "ApplyGrant", err)
	res, err := approvals.Evaluate(context.Background(), action)
	testutil.FailErr(t, "Evaluate", err)
	if res == nil || res.Required() {
		t.Fatalf("exact-action lease must silence detection, got %+v", res)
	}
}

type stubDetSource struct {
	match hitl.DetectionMatch
	ok    bool
}

func (s stubDetSource) MatchAction(hitl.ProposedAction, gate.Posture) (hitl.DetectionMatch, bool) {
	return s.match, s.ok
}

func (s stubDetSource) Escalates(match hitl.DetectionMatch, posture gate.Posture) bool {
	switch match.Level {
	case "high", "critical":
		return true
	case "medium":
		return posture == gate.PostureStrict
	default:
		return false
	}
}

func (m *coalesceHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (m *failOnceHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
