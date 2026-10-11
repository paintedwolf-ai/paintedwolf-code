package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// StoreDeps wires an in-memory store with an attached project and unchanged history.
func StoreDeps(st *store.Memory) PromptLoopDeps {
	return PromptLoopDeps{
		Context: ContextDeps{
			Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
		},
		Projection: ProjectionDeps{
			AppendMessages: st.AppendMessages,
			UpdateMessage: func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
				_, err := st.UpdateMessage(ctx, sessionID, messageID, msg)
				return err
			},
			Streams: &testMessageStreams{
				project: func(ctx context.Context, sessionID string, msg api.Message) error {
					return st.PatchLiveProjection(ctx, sessionID, msg.ID, msg.Content, msg.ToolCalls)
				},
			},
			AppendDraftVersion: st.AppendDraftVersion,
			CountDraftVersions: st.CountDraftVersions,
		},
	}
}
