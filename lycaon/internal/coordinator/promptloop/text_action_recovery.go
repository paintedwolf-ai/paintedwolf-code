package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *turnCloseout) rejectTextAction(ctx context.Context, sess *api.Session, names []string) (*guidance.Refusal, error) {
	if len(names) == 0 || sess == nil || sess.IsWorkerChild() {
		return nil, nil
	}
	if l.Deps.EvaluateCloseoutBlock == nil {
		return nil, fmt.Errorf("text action recovery OAR not configured")
	}
	gc := oar.NewGuardContext()
	gc.Rejection.RejectObservation = "text_action_envelope"
	gc.ObservationData = map[string]any{"offenders_sample": strings.Join(names, ", ")}
	decision, err := l.Deps.EvaluateCloseoutBlock(ctx, sess, gc)
	if err != nil || decision == nil {
		return nil, err
	}
	body, err := guidance.RenderPolicyCopy(ctx, decision.Code, string(decision.Effect), decision.Copy)
	if err != nil {
		return nil, err
	}
	return guidance.NewRefusal(decision.Code, body).WithPolicyCopy(decision.Copy).WithDetails(decision.Data, nil), nil
}
