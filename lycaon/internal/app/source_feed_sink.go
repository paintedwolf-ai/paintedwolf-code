package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/pkg/api"
)

// outboxSourceFeed queues source events durably.
type outboxSourceFeed struct {
	outbox *eventoutbox.Outbox
	lookup events.ProjectLookup
}

func (f outboxSourceFeed) SourceChanged(ctx context.Context, ev api.SourceChangesEvent) error {
	return f.outbox.Enqueue(ctx, api.EventTopicSourceChanged, f.key(ctx, ev), ev)
}

func (f outboxSourceFeed) SourceChangedTx(ctx context.Context, tx *sql.Tx, ev api.SourceChangesEvent) error {
	// Source events route by workspace; session ids attribute changes.
	key := events.PublishKey{
		Project: strings.TrimSpace(ev.ProjectID),
		Facet:   sourceChangesFacet(ev),
	}
	return f.outbox.EnqueueTx(ctx, tx, api.EventTopicSourceChanged, key, ev)
}

func (f outboxSourceFeed) Deliver() { f.outbox.Notify() }

func (f outboxSourceFeed) key(ctx context.Context, ev api.SourceChangesEvent) events.PublishKey {
	key := events.PublishKeyFor(ctx, f.lookup, ev.ProjectID, "")
	// Facets coalesce repeated updates per path.
	key.Facet = sourceChangesFacet(ev)
	return key
}

func sourceChangesFacet(ev api.SourceChangesEvent) string {
	prefix := string(ev.WorkspaceKind) + "\x00" + ev.WorkspaceID
	if len(ev.Changes) == 1 {
		return prefix + "\x00" + ev.Changes[0].RootID + "\x00" + ev.Changes[0].Path
	}
	digest := sha256.New()
	for _, change := range ev.Changes {
		_, _ = digest.Write([]byte(change.RootID + "\x00" + change.Path + "\x00" + string(change.Op) + "\x00"))
	}
	return prefix + "\x00batch\x00" + hex.EncodeToString(digest.Sum(nil))[:16]
}

var _ sourcefeed.Sink = outboxSourceFeed{}
