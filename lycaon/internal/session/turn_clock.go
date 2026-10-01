package session

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// beginTurnClock starts the root's clock for one execution and returns its stop
// func. A new root user turn resets the clock; continuations accrue to it. Only
// the reset and the idle/running edges publish and persist.
func (m *Manager) beginTurnClock(ctx context.Context, sessionID string, newUserTurn bool) func() {
	if m == nil {
		return func() {}
	}
	root := RootSessionID(ctx, m.store, sessionID)
	opens := newUserTurn && root == sessionID
	if opens {
		progress.OpenTurn(root)
	} else {
		m.restoreTurnClock(ctx, root)
	}
	if started := progress.TurnStarted(root); started || opens {
		m.recordTurnClock(ctx, root)
	}
	return func() {
		if progress.TurnFinished(root) {
			// Turns usually end by cancellation; the stop edge must still land.
			m.recordTurnClock(context.WithoutCancel(ctx), root)
		}
	}
}

// anchorTurnClock names the prompt that opened a root session's visible user
// turn once that message is in the transcript.
func (m *Manager) anchorTurnClock(ctx context.Context, sessionID, openingMessageID string) {
	if m == nil || strings.TrimSpace(openingMessageID) == "" {
		return
	}
	root := RootSessionID(ctx, m.store, sessionID)
	if root != sessionID {
		return
	}
	progress.AnchorTurn(root, openingMessageID)
	m.recordTurnClock(ctx, root)
}

// TurnClock returns a root session's visible user turn clock, restoring the
// durable clock when this process has not run the session.
func (m *Manager) TurnClock(ctx context.Context, root string) api.TurnClock {
	m.restoreTurnClock(ctx, root)
	return progress.TurnClockWire(root, progress.Clock(root))
}

// BeginCheckpointWait pauses a session tree's work clock while one of its
// executions waits on a person's decision; the returned func resumes it.
func (m *Manager) BeginCheckpointWait(ctx context.Context, sessionID string) func() {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}
	}
	root := RootSessionID(ctx, m.store, sessionID)
	progress.WaitStarted(root)
	return func() { progress.WaitFinished(root) }
}

func (m *Manager) recordTurnClock(ctx context.Context, root string) {
	m.publishTurnClock(ctx, root)
	m.persistTurnClock(ctx, root)
}

func (m *Manager) publishTurnClock(ctx context.Context, root string) {
	if m == nil || m.events == nil || root == "" {
		return
	}
	m.events.PublishTurnClock(ctx, progress.TurnClockWire(root, progress.Clock(root)))
}

// persistTurnClock writes the root's current clock. Reading it under the lock
// keeps a stale snapshot from landing last.
func (m *Manager) persistTurnClock(ctx context.Context, root string) {
	if m == nil || m.store == nil || root == "" {
		return
	}
	lock := m.promptState.Clock.Acquire(root)
	lock.Lock()
	defer lock.Unlock()
	clock := progress.Clock(root)
	if clock.OpeningMessageID == "" {
		return
	}
	durable := store.TurnClock{
		SessionID:        root,
		OpeningMessageID: clock.OpeningMessageID,
		ActiveMs:         clock.ActiveMs,
		WorkMs:           clock.WorkMs,
		RunningAt:        optionalTime(clock.RunningAt),
		SettledAt:        optionalTime(clock.SettledAt),
	}
	if err := m.store.PutTurnClock(ctx, durable); err != nil {
		slog.WarnContext(ctx, "persist turn clock", "session_id", root, "error", err)
	}
}

// restoreTurnClock seeds an unclocked root from its newest durable clock.
func (m *Manager) restoreTurnClock(ctx context.Context, root string) {
	if m == nil || m.store == nil || root == "" {
		return
	}
	if current := progress.Clock(root); current.OpeningMessageID != "" || current.Running() || current.ActiveMs > 0 {
		return
	}
	durable, ok, err := m.store.LatestTurnClock(ctx, root)
	if err != nil {
		slog.WarnContext(ctx, "restore turn clock", "session_id", root, "error", err)
		return
	}
	if !ok {
		return
	}
	restored := progress.TurnClock{
		OpeningMessageID: durable.OpeningMessageID,
		ActiveMs:         durable.ActiveMs,
		WorkMs:           durable.WorkMs,
	}
	if durable.SettledAt != nil {
		restored.SettledAt = *durable.SettledAt
	}
	progress.RestoreTurnClock(root, restored)
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
