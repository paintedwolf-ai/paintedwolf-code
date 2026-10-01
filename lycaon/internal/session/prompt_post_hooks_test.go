package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newPostHookManager(t *testing.T) *Manager {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	mgr := &Manager{rejectFmt: guidance.NewStaticRejectFormatter(hints)}
	mgr.ensureCoordinatorRuntime()
	return mgr
}

func pendingProgressMissingNudge(mgr *Manager, sessionID string) bool {
	return anchor.SameInform(mgr.ensureCoordinatorRuntime().Kicks().TakePendingKickID(sessionID), anchor.ProgressMissing)
}

type postHookWorkflowContext struct {
	wf feedback.WorkflowEvaluationContext
}

func (s postHookWorkflowContext) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	runCtx := api.CoordinatorRunContext{
		WorkflowID:         s.wf.WorkflowID,
		CurrentPhase:       s.wf.CurrentPhase,
		RunStatus:          string(api.WorkflowRunStatusRunning),
		AdvanceWhenGateMet: s.wf.AdvanceWhenGateMet,
	}
	frame := inject.CoordinatorTurnFrame{RunContext: runCtx}
	frame.Runtime.PhaseExit = &inject.PhaseExitView{Kind: s.wf.PhaseExitKind}
	if s.wf.CurrentGatesKnown {
		frame.Runtime.Phases = []inject.WorkflowPhaseRow{{ID: s.wf.CurrentPhase}}
		if !s.wf.CurrentGatesPassed {
			for _, leaf := range s.wf.FailedLeaves {
				frame.Runtime.Phases[0].Gates = append(frame.Runtime.Phases[0].Gates, inject.WorkflowGateState{ID: leaf})
			}
		} else {
			frame.Runtime.Phases[0].Gates = []inject.WorkflowGateState{{ID: "ready", Satisfied: true}}
		}
	}
	return frame, nil
}

func TestProgressMissingNudgesOnDispatchWithoutPlan(t *testing.T) {
	mgr := newPostHookManager(t)
	mgr.progress = progress.NewMemoryStore()

	mgr.maybeNudgeProgressMissing(t.Context(), "root-1", []string{"task"})
	if !pendingProgressMissingNudge(mgr, "root-1") {
		t.Fatal("expected PROGRESS_MISSING after task() without plan")
	}
}

func TestProgressMissingSkipsAfterRejectedDispatchTurn(t *testing.T) {
	mgr := newPostHookManager(t)
	mgr.progress = progress.NewMemoryStore()

	mgr.maybeNudgeProgressMissing(t.Context(), "root-1", []string{"list_dir"})
	if pendingProgressMissingNudge(mgr, "root-1") {
		t.Fatal("plan missing nudge only fires on the turn that attempts dispatch")
	}
}

func TestProgressMissingSkipsWorkerChild(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	ctx := t.Context()
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "proj-1")
	testutil.FailErr(t, "Create parent", err)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	mgr.store = mem
	mgr.progress = progress.NewMemoryStore()

	mgr.maybeNudgeProgressMissing(ctx, child.ID, []string{"task"})
	if pendingProgressMissingNudge(mgr, child.ID) {
		t.Fatal("worker children must not receive coordinator progress-missing informs")
	}
	if err := mgr.afterPrompt(ctx, child.ID, orchestration.ProfileImplementer, []string{"task"}); err != nil {
		testutil.FailErr(t, "afterPrompt", err)
	}
	if pendingProgressMissingNudge(mgr, child.ID) {
		t.Fatal("afterPrompt must skip coordinator lifecycle on worker children")
	}
}

func TestProgressMissingSkipsSpecWorkflowArtifacts(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureSpec}, "proj-1")
	testutil.FailErr(t, "Create session", err)
	mgr.store = mem
	mgr.progress = progress.NewMemoryStore()

	mgr.maybeNudgeProgressMissing(t.Context(), sess.ID, []string{"write"})
	if pendingProgressMissingNudge(mgr, sess.ID) {
		t.Fatal("spec workflow artifact writes must not require a build checklist")
	}
}

func TestWorkflowPhaseExitNudgesAfterUnrelatedToolArc(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, "proj-1")
	testutil.FailErr(t, "Create session", err)
	mgr.store = mem
	mgr.coordinatorFrame = postHookWorkflowContext{wf: feedback.WorkflowEvaluationContext{
		WorkflowID:         "example",
		CurrentPhase:       "intake",
		RunActive:          true,
		AdvanceWhenGateMet: "coordinator",
		CurrentGatesKnown:  true,
		CurrentGatesPassed: true,
		PhaseExitKind:      "proof",
	}}

	if !mgr.maybeNudgeWorkflowPhaseExit(t.Context(), sess.ID, orchestration.ProfileCoordinator, []string{"read"}) {
		t.Fatal("expected phase-exit nudge")
	}
	if id := mgr.ensureCoordinatorRuntime().Kicks().TakePendingKickID(sess.ID); !anchor.SameInform(id, anchor.PhaseExitRequired) {
		t.Fatalf("kick id = %q", id)
	}
}

func TestWorkflowPhaseExitAllowsAnotherAsk(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, "proj-1")
	testutil.FailErr(t, "Create session", err)
	mgr.store = mem
	mgr.coordinatorFrame = postHookWorkflowContext{wf: feedback.WorkflowEvaluationContext{
		WorkflowID:         "example",
		CurrentPhase:       "intake",
		RunActive:          true,
		AdvanceWhenGateMet: "coordinator",
		CurrentGatesKnown:  true,
		CurrentGatesPassed: true,
		PhaseExitKind:      "proof",
	}}

	if mgr.maybeNudgeWorkflowPhaseExit(t.Context(), sess.ID, orchestration.ProfileCoordinator, []string{"ask_user"}) {
		t.Fatal("another structured ask must keep coordinator intake open")
	}
}
