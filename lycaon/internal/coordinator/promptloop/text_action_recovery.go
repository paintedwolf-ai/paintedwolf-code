package promptloop

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/actionenvelope"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
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

// resolveToolCallSupport retains valid provider recovery and routes recognized
// coordinator syntax failures into the normal rejected-draft lifecycle.
func (l *modelTurn) resolveToolCallSupport(sess *api.Session, req modelcall.CompletionRequest, delivered *modelcall.Completion, providerID, model string) (*modelcall.Completion, error) {
	completion, err := llm.EnforceToolCallSupport(l.providerProfile(providerID), req, delivered, providerID, model)
	var proseError *failure.ProviderToolCallsInProseError
	if errors.As(err, &proseError) && sess != nil && !sess.IsWorkerChild() && delivered != nil && len(delivered.ToolCalls) == 0 && req.ToolsCallable() && len(actionenvelope.Names(delivered.Content, offeredToolNames(req.Tools))) > 0 {
		return delivered, nil
	}
	return completion, err
}
