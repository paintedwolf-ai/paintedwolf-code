package promptloop

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *turnProjection) assertCoordinatorCompletion(ctx context.Context, sess *api.Session, sessionID string, history []api.Message, msg api.Message) error {
	if sess == nil || sess.IsWorkerChild() || len(msg.ToolCalls) != 0 || l.Closeout.Deps.AssertCompletion == nil {
		return nil
	}
	if err := l.Closeout.Deps.AssertCompletion(ctx, sess); err != nil {
		reject, ok := guidance.RefusalFromError(err)
		if !ok {
			reject = guidance.NewRefusal("", err.Error())
		}
		_, retractErr := l.Closeout.Tools.retractRejectedAssistantTurn(ctx, sessionID, history, msg.ID, "", reject)
		return errors.Join(err, retractErr)
	}
	return nil
}
