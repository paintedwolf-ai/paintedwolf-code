package store

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func currentUserTurnLocked(s *memoryCore, sessionID string) int {
	for i := len(s.messages[sessionID]) - 1; i >= 0; i-- {
		if n := s.userTurns[sessionID][s.messages[sessionID][i].ID]; n > 0 {
			return n
		}
	}
	return 0
}

// UserIntentBefore reads the latest human opener at a worker's creation boundary.
func (t *sqlTurnTelemetry) UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error) {
	value, err := t.core.queries.UserIntentBefore(ctx, db.UserIntentBeforeParams{SessionID: sessionID, Ts: db.FormatTime(before)})
	if db.IsNoRows(err) {
		return before, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return db.ParseTime(value)
}

func (t *memoryTurnTelemetry) UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error) {
	s := t.core
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	messages := s.messages[sessionID]
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if api.IsUserIntentMessage(msg) && msg.Origin == api.MessageOriginUser && !msg.CreatedAt.After(before) {
			return msg.CreatedAt, nil
		}
	}
	return before, nil
}

// PutTurnClock records a visible user turn's clock once its opening message exists.
func (t *memoryTurnTelemetry) PutTurnClock(ctx context.Context, clock TurnClock) error {
	s := t.core
	if err := ctx.Err(); err != nil {
		return err
	}
	clock = normalizeTurnClock(clock)
	if clock.SessionID == "" || clock.OpeningMessageID == "" {
		return fmt.Errorf("turn clock requires a session and opening message")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := messageOrdLocked(s, clock.SessionID, clock.OpeningMessageID); !ok {
		return nil
	}
	clocks := s.turnClocks[clock.SessionID]
	if clocks == nil {
		clocks = make(map[string]TurnClock)
		s.turnClocks[clock.SessionID] = clocks
	}
	clocks[clock.OpeningMessageID] = cloneTurnClock(clock)
	return nil
}

// LatestTurnClock returns the clock of the session's newest visible user turn.
func (t *memoryTurnTelemetry) LatestTurnClock(ctx context.Context, sessionID string) (TurnClock, bool, error) {
	s := t.core
	if err := ctx.Err(); err != nil {
		return TurnClock{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest TurnClock
	latestOrd := int64(-1)
	for openingID, clock := range s.turnClocks[sessionID] {
		if ord, ok := messageOrdLocked(s, sessionID, openingID); ok && ord > latestOrd {
			latest, latestOrd = clock, ord
		}
	}
	return cloneTurnClock(latest), latestOrd >= 0, nil
}

// SettleAbandonedTurnClocks pauses clocks left running by process loss.
func (t *memoryTurnTelemetry) SettleAbandonedTurnClocks(ctx context.Context) (int, error) {
	s := t.core
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settled := 0
	for sessionID, clocks := range s.turnClocks {
		lastProgress := latestTurnProgressLocked(s, sessionID)
		for openingID, clock := range clocks {
			if clock.RunningAt == nil {
				continue
			}
			clocks[openingID] = clock.settleAbandoned(lastProgress)
			settled++
		}
	}
	return settled, nil
}

func turnClocksForMessagesLocked(s *memoryCore, sessionID string, window []api.Message) map[string]api.TurnClock {
	clocks := s.turnClocks[sessionID]
	out := map[string]api.TurnClock{}
	for _, message := range window {
		if clock, ok := clocks[message.ID]; ok {
			out[message.ID] = clock.Wire()
		}
	}
	return out
}

func messageOrdLocked(s *memoryCore, sessionID, messageID string) (int64, bool) {
	for _, message := range s.messages[sessionID] {
		if message.ID == messageID {
			return message.Ord, true
		}
	}
	return 0, false
}

// LatestTurnProgress is when any of the session's turns last did durable work.
func (t *memoryTurnTelemetry) LatestTurnProgress(ctx context.Context, sessionID string) (time.Time, error) {
	s := t.core
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return latestTurnProgressLocked(s, sessionID), nil
}

func latestTurnProgressLocked(s *memoryCore, sessionID string) time.Time {
	var latest time.Time
	for _, turn := range s.turns {
		if turn.SessionID == sessionID && turn.ProgressedAt.After(latest) {
			latest = turn.ProgressedAt
		}
	}
	return latest
}

func cloneTurnClock(clock TurnClock) TurnClock {
	if clock.RunningAt != nil {
		since := *clock.RunningAt
		clock.RunningAt = &since
	}
	if clock.SettledAt != nil {
		settled := *clock.SettledAt
		clock.SettledAt = &settled
	}
	return clock
}
