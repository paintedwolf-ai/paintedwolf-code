package promptloop

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// closeoutRepairNeeded binds repair to the exact rejected assistant occurrence.
type closeoutRepairNeeded struct {
	refusal   *guidance.Refusal
	messageID string
}

func (e *closeoutRepairNeeded) Error() string { return e.refusal.Error() }
func (e *closeoutRepairNeeded) Unwrap() error { return e.refusal }

func (l *PromptLoop) prepareCloseoutRepair(ctx context.Context, setup *promptLoopSetup, err error) (bool, error) {
	var repair *closeoutRepairNeeded
	if !errors.As(err, &repair) || setup.sess.IsWorkerChild() {
		return false, nil
	}
	st := setup.state
	history := make([]api.Message, 0, len(st.history))
	for _, message := range st.history {
		if message.ID != repair.messageID {
			history = append(history, message)
		}
	}
	var appendErr error
	st.history, appendErr = l.Nudges.appendHostNudge(ctx, setup.sessionID, history, HostNudge{
		Content:  repair.refusal.Body,
		Feedback: &api.ToolFeedback{Code: repair.refusal.Code(), Details: repair.refusal.Facts.FeedbackFor(repair.refusal.Code()).Details},
	}, api.MessageKindCoordinatorGuidance, st)
	if appendErr != nil {
		return false, appendErr
	}
	// One acting round followed by a bounded closeout. Run applies this once;
	// normal spend, permission, runnable-state and completion guards still apply.
	setup.completed += st.turnsRanThisRun
	setup.maxIter = setup.completed + 2
	st.progress().SetMaxToolLoops(setup.maxIter)
	st.proseFinish = false
	st.proseFinishDelivered = false
	st.lastAssistantID = ""
	st.lastAssistantContent = ""
	return true, nil
}
