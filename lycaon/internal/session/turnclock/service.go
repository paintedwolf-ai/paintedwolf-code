package turnclock

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	sessiontree.Reader
	LatestTurnClock(context.Context, string) (store.TurnClock, bool, error)
	PutTurnClock(context.Context, store.TurnClock) error
}

type Service struct {
	store     Store
	locks     promptstate.MutexRegistry
	Publisher *events.Publisher
}

func New(store Store) *Service { return &Service{store: store} }

// Begin starts the root's clock for one execution and returns its stop
// func. A new root user turn resets the clock; continuations accrue to it. Only
// the reset and the idle/running edges publish and persist.
func (m *Service) Begin(ctx context.Context, sessionID string, newUserTurn bool) func() {
	if m == nil {
		return func() {}
	}
	root := sessiontree.RootID(ctx, m.store, sessionID)
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

// Anchor names the prompt that opened a root session's visible user
// turn once that message is in the transcript.
func (m *Service) Anchor(ctx context.Context, sessionID, openingMessageID string) {
	if m == nil || strings.TrimSpace(openingMessageID) == "" {
		return
	}
	root := sessiontree.RootID(ctx, m.store, sessionID)
	if root != sessionID {
		return
	}
	progress.AnchorTurn(root, openingMessageID)
	m.recordTurnClock(ctx, root)
}

// Read returns a root session's visible user turn clock, restoring the
// durable clock when this process has not run the session.
func (m *Service) Read(ctx context.Context, root string) api.TurnClock {
	m.restoreTurnClock(ctx, root)
	return progress.TurnClockWire(root, progress.Clock(root))
}

// Wait pauses a session tree's work clock while one of its
// executions waits on a person's decision; the returned func resumes it.
func (m *Service) Wait(ctx context.Context, sessionID string) func() {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}
	}
	root := sessiontree.RootID(ctx, m.store, sessionID)
	progress.WaitStarted(root)
	return func() { progress.WaitFinished(root) }
}

func (m *Service) recordTurnClock(ctx context.Context, root string) {
	m.publishTurnClock(ctx, root)
	m.persistTurnClock(ctx, root)
}

func (m *Service) publishTurnClock(ctx context.Context, root string) {
	if m == nil || m.Publisher == nil || root == "" {
		return
	}
	m.Publisher.PublishTurnClock(ctx, progress.TurnClockWire(root, progress.Clock(root)))
}

// persistTurnClock writes the root's current clock. Reading it under the lock
// keeps a stale snapshot from landing last.
func (m *Service) persistTurnClock(ctx context.Context, root string) {
	if m == nil || m.store == nil || root == "" {
		return
	}
	lock := m.locks.Acquire(root)
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
func (m *Service) restoreTurnClock(ctx context.Context, root string) {
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
