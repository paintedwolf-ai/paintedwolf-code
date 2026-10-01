package progress

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// BuildDigest derives the progress digest for a root session from its stored doc.
func BuildDigest(content, sessionID string) api.ProgressDigest {
	result := DeriveProgress(content, DefaultProgressCap)
	steps := result.Items
	if steps == nil {
		steps = []api.ProgressStep{}
	}
	return api.ProgressDigest{
		Steps:    steps,
		Revision: CurrentRevision(sessionID),
	}
}

// TurnClockWire projects a root session's live turn clock onto the wire.
func TurnClockWire(sessionID string, clock TurnClock) api.TurnClock {
	out := api.TurnClock{
		SessionID:        sessionID,
		OpeningMessageID: clock.OpeningMessageID,
		ActiveMs:         clock.ActiveMs,
		WorkMs:           clock.WorkMs,
		Running:          clock.Running(),
	}
	if clock.Running() {
		out.RunningAt = clock.RunningAt.UTC().Format(time.RFC3339Nano)
	}
	if !clock.SettledAt.IsZero() {
		out.SettledAt = clock.SettledAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
