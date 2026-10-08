package promptloop

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// workerBudgetAnswerPoll is how often a held worker re-reads its job for the
// coordinator's answer.
const workerBudgetAnswerPoll = 250 * time.Millisecond

// workerBudgetWatch follows a worker's ceiling and budget request across
// rounds. A live job's open request ends only by a grant, which raises the
// ceiling in the same write, or by a decline, which leaves it.
type workerBudgetWatch struct {
	// requestedAt identifies the open request the last job read carried.
	requestedAt time.Time
	// heldFor is the request the final round already waited on.
	heldFor time.Time
	// raised and declined hold an answer until the worker is told.
	raised   bool
	declined bool
}

// observe records one job read taken at a round boundary.
func (w *workerBudgetWatch) observe(task *api.WorkerTask, ceilingRaised bool) {
	open := openBudgetRequestAt(task)
	if !w.requestedAt.IsZero() && !open.Equal(w.requestedAt) && !ceilingRaised {
		w.declined = true
	}
	if ceilingRaised {
		w.raised = true
		w.declined = false
	}
	w.requestedAt = open
}

// holdDue reports a worker entering its final round with a request its
// coordinator has not answered and the final round has not yet waited on.
func (w *workerBudgetWatch) holdDue(completed, maxIter int) bool {
	return !w.requestedAt.IsZero() && completed == maxIter-1 && !w.heldFor.Equal(w.requestedAt)
}

func openBudgetRequestAt(task *api.WorkerTask) time.Time {
	if task == nil || task.BudgetRequest == nil || !workerJobLive(task.Status) {
		return time.Time{}
	}
	return task.BudgetRequest.RequestedAt
}

func workerJobLive(status api.WorkerStatus) bool {
	return status == api.WorkerStatusPending || status == api.WorkerStatusRunning
}

// awaitWorkerBudgetAnswer holds the final round until the coordinator answers
// the open request, the job stops, or the wait elapses. The request stays on
// the job after a timeout so a resume inherits it.
func (l turnNudges) awaitWorkerBudgetAnswer(ctx context.Context, sess *api.Session, st *promptLoopTurnState) {
	w := &st.workerBudget
	w.heldFor = w.requestedAt
	wait := l.Deps.WorkerBudgetAnswerWait
	if wait <= 0 || l.Deps.WorkerJob == nil {
		return
	}
	started := time.Now()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	poll := time.NewTicker(workerBudgetAnswerPoll)
	defer poll.Stop()
	outcome := "timeout"
	defer func() {
		slog.InfoContext(ctx, "worker final round held for budget answer",
			"component", "prompt_loop", "session_id", sess.ID, "worker_job_id", st.workerJobID,
			"outcome", outcome, "held_ms", time.Since(started).Milliseconds())
	}()
	for {
		task, ok := l.Deps.WorkerJob(ctx, st.workerJobID)
		if !ok || !openBudgetRequestAt(task).Equal(w.requestedAt) {
			outcome = "answered"
			return
		}
		if l.Deps.WorkerGracefulCancelPending != nil {
			if _, pending := l.Deps.WorkerGracefulCancelPending(ctx, sess); pending {
				outcome = "cancel_pending"
				return
			}
		}
		select {
		case <-ctx.Done():
			outcome = "context_done"
			return
		case <-deadline.C:
			return
		case <-poll.C:
		}
	}
}

// maybeWorkerBudgetAnswerNudge tells a worker once how its coordinator
// answered: a raised ceiling, or a decline.
func (l turnNudges) maybeWorkerBudgetAnswerNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	iterIndex, maxIter int,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if l.PromptLoop == nil || sess == nil {
		return history, nil
	}
	w := &st.workerBudget
	var render func(ctx context.Context, sess *api.Session, used, max int) HostNudge
	switch {
	case w.raised:
		render = l.Deps.WorkerBudgetRaisedNudge
	case w.declined:
		render = l.Deps.WorkerBudgetDeclinedNudge
	default:
		return history, nil
	}
	w.raised, w.declined = false, false
	if render == nil {
		return history, nil
	}
	nudge := render(ctx, sess, iterIndex, maxIter)
	if nudge.Empty() {
		return history, nil
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}
