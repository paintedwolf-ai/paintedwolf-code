package assembly

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

func (e *turnContextAssembler) skillProcedureInject(ctx context.Context, sess *api.Session, profileID string) (api.Message, bool) {
	msg, _, ok := e.skillProcedureWithTool(ctx, sess, profileID)
	return msg, ok
}
