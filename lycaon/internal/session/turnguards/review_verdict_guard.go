package turnguards

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

const reviewVerdictMissingCode = "REVIEW_VERDICT_MISSING_BEFORE_CLOSEOUT"

const verdictDelayMaxPerPrompt = 2

// Verdict repair is offered only when the coordinator can invoke tools.
func (m *Service) MissingVerdict(ctx context.Context, sess *api.Session, workersIdle bool, invokeAllowed bool) (*guidance.Refusal, bool) {
	if m == nil || sess == nil || m.workflows == nil {
		return nil, false
	}
	if !invokeAllowed {
		return nil, false
	}
	verdictPending := m.workflows.Policy.ActiveReviewVerdictPending(ctx, sess.ID)
	root := sessiontree.RootID(ctx, m.store, sess.ID)
	delayCount, delayed := m.closeouts.Delay(sess.ID, root, closeouts.VerdictDelay, workersIdle && verdictPending, verdictDelayMaxPerPrompt)
	return m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.WorkersIdle = workersIdle
		gc.ReviewVerdictGateOpen = verdictPending
		gc.VerdictDelayCount = int64(delayCount)
		if delayed {
			gc.PutRejectData(reviewVerdictMissingCode, map[string]any{})
		}
		return nil
	})
}
