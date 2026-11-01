package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectTools compiles current callable schemas without opening a turn.
func (l *PromptLoop) ProjectTools(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) ([]tools.ToolMeta, error) {
	metas, _, _, err := l.Context.coordinatorToolsForTurn(ctx, sess, frame.Machine.ProfileID, history, "", 0, 0, frame, nil)
	return metas, err
}
