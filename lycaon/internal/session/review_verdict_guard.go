package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

const reviewVerdictMissingCode = "REVIEW_VERDICT_MISSING_BEFORE_CLOSEOUT"

const verdictDelayMaxPerPrompt = 2

// Verdict repair is offered only when the coordinator can invoke tools.
func (m *Manager) maybeRejectCloseoutForMissingVerdict(ctx context.Context, sess *api.Session, workersIdle bool, invokeAllowed bool) (*guidance.Refusal, bool) {
	if m == nil || sess == nil || m.workflows == nil {
		return nil, false
	}
	if !invokeAllowed {
		return nil, false
	}
	verdictPending := m.workflows.Policy.ActiveReviewVerdictPending(ctx, sess.ID)
	root := RootSessionID(ctx, m.store, sess.ID)
	delayCount, delayed := m.closeout.delay(sess.ID, root, closeoutVerdictDelay, workersIdle && verdictPending, verdictDelayMaxPerPrompt)
	return m.tryOARFinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.WorkersIdle = workersIdle
		gc.ReviewVerdictGateOpen = verdictPending
		gc.VerdictDelayCount = int64(delayCount)
		if delayed {
			gc.PutRejectData(reviewVerdictMissingCode, map[string]any{})
		}
		return nil
	})
}
