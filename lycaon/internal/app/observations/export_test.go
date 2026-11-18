package observations

import (
	"context"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
)

func emitProgressCompletion(
	ctx context.Context,
	store *store.Repositories,
	eventPub *events.Publisher,
	progressStore progress.RunScopedStore,
	activeRun ActiveRunID,
	sessionID string,
) {
	if store == nil || eventPub == nil || progressStore == nil || activeRun == nil {
		return
	}
	emitProgressCompletionFromContent(ctx, store.Sessions, store.Transcript, eventPub, progressStore, activeRun, sessionID, progressStore.Get(ctx, sessionID))
}
