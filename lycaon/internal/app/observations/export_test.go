package observations

import (
	"context"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
)

func emitProgressCompletion(
	ctx context.Context,
	store *store.SQL,
	eventPub *events.Publisher,
	progressStore progress.RunScopedStore,
	activeRun ActiveRunID,
	sessionID string,
) {
	if store == nil || eventPub == nil || progressStore == nil || activeRun == nil {
		return
	}
	emitProgressCompletionFromContent(ctx, store, eventPub, progressStore, activeRun, sessionID, progressStore.Get(ctx, sessionID))
}
