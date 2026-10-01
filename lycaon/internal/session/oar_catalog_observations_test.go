package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// [OAR-EVAL-8] Catalog selection uses the producer's observed facts.
func requireCatalogDecision(t *testing.T, anchor string, gc *oar.GuardContext, code string) {
	t.Helper()
	result, err := sessionTestOARPipeline(t).EvaluateBlock(context.Background(), anchor, gc)
	testutil.FailErr(t, "evaluate stock catalog", err)
	got := ""
	if result != nil && result.Decision != nil && result.Decision.Effect == oar.EffectBlock {
		got = result.Decision.Code
	}
	if got != code {
		t.Fatalf("catalog block = %q, want %q", got, code)
	}
}

func TestCatalogStackedBaseUsesValidatedState(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	sess := &api.Session{ID: "catalog-stacked-parent", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator", Posture: api.SessionPostureBuild}
	job, err := q.Enqueue(ctx, api.WorkerTask{ParentSessionID: sess.ID, ProjectID: sess.ProjectID, WorkspacePath: sess.WorkspacePath, AgentType: "implementer", Prompt: "fixture", Brief: "fixture", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}})
	testutil.FailErr(t, "enqueue base", err)
	for _, tc := range []struct{ name, base, agent, mode, code string }{
		{"valid", job, "implementer", "write", ""},
		{"missing", "absent", "implementer", "write", session.OverlayBaseMissingCode},
		{"scope rejected before base lookup", "absent", "path-explorer", "write", session.TaskScopeProfileReadOnlyCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := observeTaskInFlight(t, ctx, session.WorkerCycleGuardDeps{Workers: q}, sess, map[string]any{"agent_type": tc.agent, "scope": map[string]any{"mode": tc.mode, "base_overlay_id": tc.base}})
			requireCatalogDecision(t, oar.AnchorCoordinatorPreInvoke, gc, tc.code)
		})
	}
}

func TestCatalogReadCapacityDoesNotMeanWorkersIdle(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	sess := &api.Session{ID: "catalog-cap-parent", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator", Posture: api.SessionPostureBuild}
	_, err := q.Enqueue(ctx, api.WorkerTask{ParentSessionID: sess.ID, ProjectID: sess.ProjectID, WorkspacePath: sess.WorkspacePath, AgentType: "path-explorer", Prompt: "fixture", Brief: "fixture", Scope: &api.TaskScope{Mode: api.TaskScopeModeRead}})
	testutil.FailErr(t, "enqueue read worker", err)
	for _, cap := range []int{1, 2} {
		gc := observeTaskInFlight(t, ctx, session.WorkerCycleGuardDeps{Workers: q, MaxReadWorkers: func(context.Context, string) int { return cap }}, sess, map[string]any{"agent_type": "path-explorer", "scope": map[string]any{"mode": "read"}})
		if gc.WorkersIdle || gc.ActiveReadCount != 1 || gc.ActiveWriteCount != 0 {
			t.Fatalf("roster facts: idle=%v read=%d write=%d", gc.WorkersIdle, gc.ActiveReadCount, gc.ActiveWriteCount)
		}
		code := ""
		if cap == 1 {
			code = session.CoordinatorWorkerInFlightCode
		}
		requireCatalogDecision(t, oar.AnchorCoordinatorPreInvoke, gc, code)
	}
}

type failedArtifactProbe struct{}

func (failedArtifactProbe) HasChanges(context.Context, string) (bool, error) {
	return false, errors.New("workspace probe unavailable")
}

func TestCatalogArtifactProbeFailureIsNotAbsence(t *testing.T) {
	sess := &api.Session{ID: "artifact-probe-child", ParentSessionID: "parent", AgentType: "implementer", WorkspacePath: t.TempDir()}
	history := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "read-1", Name: "read"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "edit-1", Name: "edit"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeRejected}},
	}
	gc := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(context.Background(), sess, history, "Report", sess.WorkspacePath, failedArtifactProbe{}, gc)
	if gc.WorkerArtifactMeasured {
		t.Fatal("failed probe claimed a measurement")
	}
	requireCatalogDecision(t, oar.AnchorWorkerReportCheck, gc, "")
}

func TestCatalogGroundingVerdictsDoNotOverlap(t *testing.T) {
	for _, code := range []string{"COORDINATOR_UNGROUNDED_CLAIM", "COORDINATOR_CRITERIA_UNMET", "COORDINATOR_GROUNDING_ESCALATED"} {
		t.Run(code, func(t *testing.T) {
			gc := oar.NewGuardContext()
			delegation.ObserveDelegationGroundingVerdict(gc, delegation.GroundingVerdict{Code: code, Reason: "Missing {{ receipt }} — résumé", LegID: "fixture-leg"})
			if code == "COORDINATOR_GROUNDING_ESCALATED" {
				result, err := sessionTestOARPipeline(t).EvaluateBlock(t.Context(), oar.AnchorCoordinatorPostTurn, gc)
				testutil.FailErr(t, "evaluate escalation notice", err)
				if result.Decision == nil || result.Decision.Effect != oar.EffectNudge || len(result.Decision.Advisories) != 1 || result.Decision.Advisories[0].Code != code {
					t.Fatalf("post-turn advisory = %+v", result.Decision)
				}
			} else {
				result, err := sessionTestOARPipeline(t).EvaluateBlock(t.Context(), oar.AnchorCoordinatorCloseoutCheck, gc)
				testutil.FailErr(t, "evaluate grounding refusal", err)
				if result.Decision == nil || result.Decision.Code != code || result.Decision.Effect != oar.EffectBlock {
					t.Fatalf("closeout decision = %+v", result.Decision)
				}
				if result.Decision.Copy == nil || result.Decision.Copy["cause"] != "Missing {{ receipt }} — résumé" {
					t.Fatalf("grounding reason was lost or interpreted: %+v", result.Decision.Copy)
				}
			}
		})
	}
}
