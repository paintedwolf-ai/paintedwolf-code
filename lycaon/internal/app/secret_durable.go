package app

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/secretmatch"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireMessageSecretRedaction(matcher *secretmatch.Matcher) {
	// Store and pre-spill projection share the same evidence and field walker.
	redact := func(ctx context.Context, msg wire.Message) (wire.Message, bool) {
		return llm.RedactMessageForStorage(ctx, matcher, msg)
	}
	sessionstore.SetMessageRedactor(redact)
	if b.mgr != nil {
		b.mgr.SetMessageStorageRedactor(redact)
	}
	if b.secretCaps != nil && b.mgr != nil {
		b.secretCaps.AddScreeningInvalidationObserver(func(_ context.Context, projectID string) {
			// Scheduling runs after writer release and completes within the request drain.
			b.sweepManagedSecretTrees(b.ctx, projectID) //nolint:contextcheck // Committed protection survives request cancellation.
		})
		b.sweepManagedSecretTrees(b.ctx, "")
	}
}

func (b *serveBuilder) sweepManagedSecretTrees(ctx context.Context, projectID string) {
	rows, err := db.New(b.db).ListSessions(ctx)
	if err != nil {
		slog.WarnContext(ctx, "could not refresh managed secret transcript screening", "error", err)
		return
	}
	for _, row := range rows {
		if row.ParentSessionID.Valid || (projectID != "" && row.ProjectID != projectID) {
			continue
		}
		if len(b.secretCaps.DurableScreeningValues(row.ProjectID)) > 0 {
			b.secretHarvest.Invalidate(row.ID)
		}
	}
}
