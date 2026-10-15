package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *modelTurn) emptyResponseRecovery(sess *api.Session, req modelcall.CompletionRequest) func(context.Context) (string, error) {
	if req.ToolProtocol != modelcall.ToolProtocolNative {
		return nil
	}
	observation := oar.ContentObservation{EmptyResponse: &oar.EmptyResponseObservation{
		ToolsCallable:    req.ToolsCallable(),
		StructuredOutput: req.ResponseFormat != nil,
	}}
	return func(ctx context.Context) (string, error) {
		reject, blocked, _, _ := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentOutput, observation, "", nil)
		if !blocked || reject == nil {
			return "", nil
		}
		if reject.Code() != "MODEL_RESPONSE_EMPTY" {
			return "", reject
		}
		return reject.Body, nil
	}
}
