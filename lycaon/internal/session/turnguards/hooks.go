package turnguards

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workercontext"
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
	var identity workeradmission.TaskIdentity
	if tool == "task" && sess != nil && surface.IsCoordinatorParent(sess) {
		var err error
		identity, err = m.resolveTaskIdentity(ctx, sess, args)
		if err != nil {
			return "", false, err
		}
	}
	implState := m.state.ForSession(ctx, sess)
	surfaceID := m.surface.PromptTurnSurfaceID(sess.ID)
	progressState := m.progressPrerequisites(ctx, sess)
	root, currentProgress, reviewLoopActive := progressState.RootSessionID, progressState.Content, progressState.ReviewLoopActive
	closureBaseline, closureArmed := m.ProgressClosure.Baseline(root)
	guardDeps := m.WorkerAdmission()
	declaredVerify := ""
	if tool == "verify" {
		declaredVerify = m.Verification.SourceVerifyCommand(ctx, m.Workspace.SettingsPath(ctx, sess))
		guard.PrepareVerifyCall(tool, declaredVerify, args)
	}
	if reject, skip, err := m.ToolPolicy.Block(ctx, oar.AnchorCoordinatorPreInvoke, sess, tool, args, func(gc *oar.GuardContext) error {
		guard.ObserveWaitVisibility(sess, history, tool, surface.PublishesMidTurnText(surfaceID), gc)
		if gc.Session.UnpublishedToolTextPending {
			gc.Session.MidTurnEvidenceUnreadable = m.midTurnEvidenceUnreadable(ctx, sess.ID, history)
		}
		guard.ObserveTaskWhilePendingUserInput(sess, implState, tool, gc)
		guard.ObserveCoordinatorWorkerBranchPath(sess, tool, args, gc)
		guard.ObserveProgressMissingBeforeDispatch(sess, currentProgress, tool, reviewLoopActive, gc)
		guard.ObserveProgressItemNotClosedBeforeDispatch(sess, currentProgress, tool, closureBaseline, closureArmed, gc)
		guard.ObserveCoordinatorBatchPhaseTool(sess, tool, implState, m.Batch.TurnGuard(sess.ID), gc)
		guard.ObserveCoordinatorSynthesisWrapupTool(surfaceID, tool, tools.ToolOffered(ctx, tool), gc)
		guard.ObserveVerifyCommandUndeclared(tool, declaredVerify, args, gc)
		guard.ObserveProgressReconcileOnSynthesis(surfaceID, currentProgress, args, gc)
		return workeradmission.ObserveCoordinatorTaskInFlight(ctx, guardDeps, sess, tool, identity, gc)
	}); skip || err != nil {
		if reject != nil {
			return "", false, reject
		}
		return "", skip, err
	}
	return "", false, nil
}

func (m *Service) BeforeFinish(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	_ string,
	lastAssistant string,
	draft guidance.CloseoutDraft,
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
		return m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
			if invokeAllowed {
				if _, _, accepted := workercompletion.AcceptedWorkerReport(history, workercontext.Job(ctx)); workercontext.Job(ctx) == "" || !accepted {
					gc.Rejection.RejectObservation = "worker_report_required"
				}
			}
			return nil
		})
	}
	if reject, block := m.PendingOverlays(ctx, sess, invokeAllowed); block {
		return reject, true
	}
	implState := m.state.ForSession(ctx, sess)
	openProgressChecked := false
	if invokeAllowed && workersIdle && len(implState.PendingOverlayIDs) == 0 && guard.SurfaceFinishesWithUserProse(surfaceID) && implState.ProgressOpenCount > 0 {
		openProgressChecked = true
		reject, block, waived := m.openProgressWithWaiver(
			ctx, sess, history, surfaceID, workersIdle, implState, invokeAllowed,
		)
		if block {
			return reject, true
		}
		if waived {
			m.waiveOpenProgress(ctx, sess)
			implState.ProgressOpenCount = 0
		}
	}
	if invokeAllowed {
		var batchTurn guard.BatchTurnGuard
		if m.Batch != nil {
			batchTurn = m.Batch.TurnGuard(sess.ID)
		}
		if reject, block := m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
			guard.ObserveCoordinatorHostNoToolTurn(
				sess, history, lastAssistant, draft, turnTools, surfaceID, workersIdle, implState, batchTurn, gc,
			)
			return nil
		}); block {
			return reject, true
		}
	}
	if reject, block := m.MissingVerdict(ctx, sess, workersIdle, invokeAllowed); block {
		return reject, true
	}
	if reject, block := m.BeforeReportPhase(ctx, sess, draft, surfaceID, invokeAllowed); block {
		return reject, true
	}
	if reject, block := m.SourceEvidence(ctx, sess, history, draft.Verification, surfaceID, invokeAllowed); block {
		return reject, true
	}
	if reject, block := m.OpenGates(ctx, sess, workersIdle, invokeAllowed); block {
		return reject, true
	}
	if openProgressChecked {
		return nil, false
	}
	return m.OpenProgress(
		ctx, sess, history, surfaceID, workersIdle, implState, invokeAllowed,
	)
}

// midTurnEvidenceUnreadable repeats publication's read, so wait feedback can tell a
// host failure from a citation defect. A host with no ledger wired has no such failure.
func (m *Service) midTurnEvidenceUnreadable(ctx context.Context, sessionID string, history []api.Message) bool {
	if m.Verification == nil || m.Verification.Evidence == nil {
		return false
	}
	_, _, err := guidance.MidTurnEvidence(ctx, m.Verification.Evidence, sessionID, history)
	return err != nil
}
