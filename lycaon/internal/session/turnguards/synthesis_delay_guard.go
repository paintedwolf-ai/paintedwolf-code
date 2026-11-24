package turnguards

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

const progressOpenBeforeCloseoutCode = "PROGRESS_OPEN_BEFORE_CLOSEOUT"

const synthesisDelayMaxPerPrompt = 2

// OpenProgress checks whether open progress rows block closeout on non-synthesis surfaces.
func (m *Service) OpenProgress(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string, workersIdle bool, implState surface.ImplementSessionState, invokeAllowed bool) (*guidance.Refusal, bool) {
	reject, block, _ := m.openProgressWithWaiver(ctx, sess, history, surfaceID, workersIdle, implState, invokeAllowed)
	return reject, block
}

func (m *Service) openProgressWithWaiver(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string, workersIdle bool, implState surface.ImplementSessionState, invokeAllowed bool) (*guidance.Refusal, bool, bool) {
	if m == nil || m.progress == nil || sess == nil {
		return nil, false, false
	}
	if !invokeAllowed {
		return nil, false, false
	}
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == spawn.SurfaceImplementSynthesis || !guard.SurfaceFinishesWithUserProse(surfaceID) {
		return nil, false, false
	}
	root := sessiontree.RootID(ctx, m.store, sess.ID)
	var snapshot progress.Snapshot
	if s, err := m.progress.Snapshot(ctx, root); err == nil {
		snapshot = s
	}
	var verifyRequired, verifyPassed, verifyUnverified bool
	if m.Verification != nil {
		verifyRequired, verifyPassed, _, verifyUnverified = m.Verification.WorkflowGateState(ctx, sess, history)
	}
	batchReady := workeroutcomes.SynthesisBlockedOnlyByOpenProgress(implState, history, snapshot, verifyRequired, verifyPassed || verifyUnverified)
	pending := snapshot.OpenStepCount()
	eligible := workersIdle && batchReady && pending > 0
	if eligible && m.closeouts != nil && m.closeouts.ProgressWaived(root, snapshot.DocumentVersion()) {
		return nil, false, true
	}
	delayCount, delayed := m.closeouts.Delay(sess.ID, root, closeouts.ProgressDelay, eligible, synthesisDelayMaxPerPrompt)
	reject, block := m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.Workers.WorkersIdle = workersIdle
		gc.Progress.ProgressOpenItems = int64(pending)
		gc.Workflow.BatchReadyIgnoringProgress = batchReady
		gc.Workflow.SynthesisDelayCount = int64(delayCount)
		if delayed {
			gc.PutRejectData(progressOpenBeforeCloseoutCode, map[string]any{"pending": pending})
		}
		return nil
	})
	return reject, block, eligible && !block
}

func (m *Service) waiveOpenProgress(ctx context.Context, sess *api.Session) {
	if m == nil || m.progress == nil || m.closeouts == nil || sess == nil {
		return
	}
	root := sessiontree.RootID(ctx, m.store, sess.ID)
	if snapshot, err := m.progress.Snapshot(ctx, root); err == nil {
		m.closeouts.WaiveProgress(root, snapshot.DocumentVersion())
	}
}
