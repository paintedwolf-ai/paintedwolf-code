package security

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/secretmatch"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *Runtime) BindTranscript(matcher *secretmatch.Matcher, redactor func(func(context.Context, wire.Message) (wire.Message, bool)), sweep func(context.Context, string, uint64)) {
	// Store and pre-spill projection share the same evidence and field walker.
	redact := func(ctx context.Context, msg wire.Message) (wire.Message, bool) {
		return llm.RedactMessageForStorage(ctx, matcher, msg)
	}
	sessionstore.SetMessageRedactor(redact)
	b.sweep = sweep
	if redactor != nil {
		redactor(redact)
	}
	if b.Capabilities != nil && sweep != nil {
		b.Capabilities.AddScreeningInvalidationObserver(func(_ context.Context, projectID string) {
			// Scheduling runs after writer release and completes within the request drain.
			b.sweepManagedSecretTrees(b.ctx, projectID) //nolint:contextcheck // Committed protection survives request cancellation.
		})
		b.sweepManagedSecretTrees(b.ctx, "")
	}
}

func (b *Runtime) sweepManagedSecretTrees(ctx context.Context, projectID string) {
	rows, err := db.New(b.database).ListSessions(ctx)
	if err != nil {
		slog.WarnContext(ctx, "could not refresh managed secret transcript screening", "error", err)
		return
	}
	for _, row := range rows {
		if row.ParentSessionID.Valid || (projectID != "" && row.ProjectID != projectID) {
			continue
		}
		if len(b.Capabilities.DurableScreeningValues(row.ProjectID)) > 0 {
			b.Harvest.Invalidate(row.ID)
		}
	}
}
