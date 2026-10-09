package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

const progressOpenBeforeCloseoutCode = "PROGRESS_OPEN_BEFORE_CLOSEOUT"

const synthesisDelayMaxPerPrompt = 2

// Synthesis skips progress-repair retries; other surfaces need tool access.
func (m *Manager) maybeRejectCloseoutForOpenProgress(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string, workersIdle bool, implState surface.ImplementSessionState, invokeAllowed bool) (*guidance.Refusal, bool) {
	if m == nil || m.progress == nil || sess == nil {
		return nil, false
	}
	if !invokeAllowed {
		return nil, false
	}
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == spawn.SurfaceImplementSynthesis || !guard.SurfaceFinishesWithUserProse(surfaceID) {
		return nil, false
	}
	root := RootSessionID(ctx, m.store, sess.ID)
	content := m.progress.Get(ctx, root)
	verifyRequired, verifyPassed, _, verifyUnverified := m.workflowVerifyGateState(ctx, sess, history)
	batchReady := SynthesisBlockedOnlyByOpenProgress(implState, history, content, verifyRequired, verifyPassed || verifyUnverified)
	_, pending, _ := progress.CloseCounts(content)
	delayCount, delayed := m.closeout.delay(sess.ID, root, closeoutProgressDelay, workersIdle && batchReady && pending > 0, synthesisDelayMaxPerPrompt)
	return m.tryOARFinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.Workers.WorkersIdle = workersIdle
		gc.Progress.ProgressOpenItems = int64(pending)
		gc.Workflow.BatchReadyIgnoringProgress = batchReady
		gc.Workflow.SynthesisDelayCount = int64(delayCount)
		if delayed {
			gc.PutRejectData(progressOpenBeforeCloseoutCode, map[string]any{"pending": pending})
		}
		return nil
	})
}
