package turnguards

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) BeforeTool(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	_ string,
	tool string,
	args map[string]any,
) (string, bool, error) {
	if reject, blocked, err := m.ToolPolicy.Block(ctx, oar.AnchorSessionPreInvoke, sess, tool, args, nil); blocked || err != nil {
		if reject != nil {
			return "", false, reject
		}
		return "", blocked, err
	}
	implState := m.state.ForSession(ctx, sess)
	surfaceID := m.surface.PromptTurnSurfaceID(sess.ID)
	root := sessiontree.RootID(ctx, m.store, sess.ID)
	currentProgress := ""
	if m.progress != nil {
		currentProgress = m.progress.Get(ctx, root)
	}
	reviewLoopActive := m.workflows != nil && m.workflows.ActivePhaseHasReviewLoop(ctx, sess.ID)
	closureBaseline, closureArmed := m.ProgressClosure.Baseline(root)
	guardDeps := m.WorkerAdmission()
	declaredVerify := ""
	if tool == "verify" {
		declaredVerify = m.Verification.SourceVerifyCommand(ctx, m.Workspace.SettingsPath(ctx, sess))
		guard.PrepareVerifyCall(tool, declaredVerify, args)
	}
	if reject, skip, err := m.ToolPolicy.Block(ctx, oar.AnchorCoordinatorPreInvoke, sess, tool, args, func(gc *oar.GuardContext) error {
		guard.ObserveTaskWhilePendingUserInput(sess, implState, tool, gc)
		guard.ObserveCoordinatorWorkerBranchPath(sess, tool, args, gc)
		guard.ObserveProgressMissingBeforeDispatch(sess, currentProgress, tool, reviewLoopActive, gc)
		guard.ObserveProgressItemNotClosedBeforeDispatch(sess, currentProgress, tool, closureBaseline, closureArmed, gc)
		guard.ObserveCoordinatorBatchPhaseTool(sess, tool, implState, m.Batch.TurnGuard(sess.ID), gc)
		guard.ObserveCoordinatorSynthesisWrapupTool(surfaceID, tool, tools.ToolOffered(ctx, tool), gc)
		guard.ObserveVerifyCommandUndeclared(tool, declaredVerify, args, gc)
		guard.ObserveProgressReconcileOnSynthesis(surfaceID, currentProgress, args, gc)
		return workeradmission.ObserveCoordinatorTaskInFlight(ctx, guardDeps, sess, tool, args, gc)
	}); skip || err != nil {
		if reject != nil {
			return "", false, reject
		}
		return "", skip, err
	}
	if closureArmed {
		m.ProgressClosure.ClearSatisfied(root, currentProgress, closureBaseline)
	}
	return "", false, nil
}

func (m *Service) BeforeFinish(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	_ string,
	lastAssistant string,
	surfaceID string,
	workersIdle bool,
	turnTools []string,
	invokeAllowed bool,
) (reject *guidance.Refusal, blocked bool) {
	if m == nil || sess == nil {
		return nil, false
	}
	if sess.IsWorkerChild() && m.decisions.DecisionPending(ctx, sess.ID) {
		return nil, false
	}
	defer func() {
		if !blocked {
			reject, blocked = m.AfterTurn(ctx, sess, lastAssistant, workersIdle)
		}
	}()
	if sess.IsWorkerChild() {
		projectDir, _ := m.Workspace.ActivePath(ctx, sess)
		return m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
			workercompletion.ObserveImplementerFinishWithoutWrite(
				ctx, sess, history, lastAssistant, projectDir, m.workspaceCheck, gc,
			)
			return nil
		})
	}
	implState := m.state.ForSession(ctx, sess)
	batchTurn := m.Batch.TurnGuard(sess.ID)
	if reject, block := m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		guard.ObserveCoordinatorHostNoToolTurn(
			sess, history, lastAssistant, turnTools, surfaceID, workersIdle, implState, batchTurn, gc,
		)
		return nil
	}); block {
		return reject, true
	}
	if reject, block := m.MissingVerdict(ctx, sess, workersIdle, invokeAllowed); block {
		return reject, true
	}
	if reject, block := m.BeforeReportPhase(ctx, sess, lastAssistant, surfaceID, invokeAllowed); block {
		return reject, true
	}
	if reject, block := m.SourceEvidence(ctx, sess, history, surfaceID, invokeAllowed); block {
		return reject, true
	}
	assessment := LatestAssessment(history)
	if !assessment.Valid() || assessment.Method != "blocked" {
		if reject, block := m.OpenGates(ctx, sess, workersIdle, invokeAllowed); block {
			return reject, true
		}
	}
	return m.OpenProgress(
		ctx, sess, history, surfaceID, workersIdle, implState, invokeAllowed,
	)
}
