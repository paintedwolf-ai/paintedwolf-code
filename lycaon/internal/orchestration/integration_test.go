package orchestration_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMultiLegDelegationDependencyOrder(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orchestration.LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)
	spec.Task = "multi-leg order"

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	if _, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	}); err != nil {
		t.Fatal(err)
	}
	if len(rec.order) != 6 {
		t.Fatalf("dispatch count = %d want 6", len(rec.order))
	}

	researchIdx := indexOf(rec.order, "research")
	planIdx := indexOf(rec.order, "plan")
	implementIdx := indexOf(rec.order, "implement")
	reviewIdx := indexOf(rec.order, "review")
	testIdx := indexOf(rec.order, "test")
	closeoutIdx := indexOf(rec.order, "closeout")
	for name, idx := range map[string]int{
		"research": researchIdx, "plan": planIdx, "implement": implementIdx,
		"review": reviewIdx, "test": testIdx, "closeout": closeoutIdx,
	} {
		if idx < 0 {
			t.Fatalf("missing stage %q in %v", name, rec.order)
		}
	}
	if researchIdx >= planIdx || planIdx >= implementIdx {
		t.Fatalf("sequential order violated: %v", rec.order)
	}
	if reviewIdx <= implementIdx || testIdx <= implementIdx {
		t.Fatalf("review/test before implement: %v", rec.order)
	}
	if closeoutIdx <= reviewIdx || closeoutIdx <= testIdx {
		t.Fatalf("closeout before parallel pair: %v", rec.order)
	}
}

func TestParallelLegsStartConcurrently(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orchestration.LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	if _, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	}); err != nil {
		t.Fatal(err)
	}

	reviewAt, okReview := rec.dispatchAt["review"]
	testAt, okTest := rec.dispatchAt["test"]
	if !okReview || !okTest {
		t.Fatalf("dispatch timestamps missing: review=%v test=%v order=%v", okReview, okTest, rec.order)
	}
	delta := reviewAt.Sub(testAt)
	if delta < 0 {
		delta = -delta
	}
	if delta > 100*time.Millisecond {
		t.Fatalf("review/test dispatch delta = %v want <=100ms", delta)
	}
}

type failStageDelegation struct {
	inner     orchestration.PipelineDelegation
	store     orchestration.PipelineDelegationStore
	failStage string
	order     []string
}

func (f *failStageDelegation) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
	leg, err := f.store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	f.order = append(f.order, leg.Title)
	if leg.Title == f.failStage {
		now := time.Now().UTC()
		leg.Status = api.LegStatusFailed
		leg.StartedAt = &now
		if err := f.store.UpdateLeg(ctx, *leg); err != nil {
			return nil, err
		}
		return leg, nil
	}
	return f.inner.DispatchLeg(ctx, delegationID, legID, sourceToolCallID)
}

func (f *failStageDelegation) Abort(ctx context.Context, delegationID, reason string) error {
	return f.inner.Abort(ctx, delegationID, reason)
}

func TestDependencyFailureBlocksDownstream(t *testing.T) {
	mockCfg, err := llm.LoadMockConfig()
	testutil.FailErr(t, "llm.LoadMockConfig failed", err)

	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(mockCfg), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	failDel := &failStageDelegation{inner: delMgr, store: delStore, failStage: "research"}

	reg := loadAgentRegistryFromConfig(t)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: failDel,
		Store:      delStore,
		Agents:     reg,
	})

	ctx := context.Background()
	spec, err := orchestration.LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	if err == nil {
		t.Fatal("expected pipeline error when research fails")
	}
	if slices.Contains(failDel.order, "plan") {
		t.Fatalf("plan dispatched after research failure: %v", failDel.order)
	}
}
