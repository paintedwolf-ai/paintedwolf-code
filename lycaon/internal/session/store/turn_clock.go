package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// TurnClock is the durable clock of one visible user turn, keyed by the
// prompt that opened it.
type TurnClock struct {
	SessionID        string
	OpeningMessageID string
	ActiveMs         int64
	WorkMs           int64
	// RunningAt is set while the clock advances.
	RunningAt *time.Time
	// SettledAt is when the clock last paused.
	SettledAt *time.Time
}

// abandonedTurnClockPageSize bounds one recovery pass over running clocks.
const abandonedTurnClockPageSize = 256

func normalizeTurnClock(clock TurnClock) TurnClock {
	clock.SessionID = strings.TrimSpace(clock.SessionID)
	clock.OpeningMessageID = strings.TrimSpace(clock.OpeningMessageID)
	clock.ActiveMs = max(clock.ActiveMs, 0)
	clock.WorkMs = min(max(clock.WorkMs, 0), clock.ActiveMs)
	return clock
}

// settleAbandoned pauses a clock whose process died while it ran. The clock
// stops at the session's last durable turn progress, never later: the time the
// host was down, and the recovery that fenced the turn, are not work.
func (c TurnClock) settleAbandoned(lastProgress time.Time) TurnClock {
	if c.RunningAt == nil {
		return c
	}
	end := *c.RunningAt
	if lastProgress.After(end) {
		end = lastProgress
	}
	elapsed := end.Sub(*c.RunningAt).Milliseconds()
	c.ActiveMs += elapsed
	c.WorkMs += elapsed
	c.RunningAt = nil
	c.SettledAt = &end
	return normalizeTurnClock(c)
}

// Wire projects the durable clock onto the transcript page shape.
func (c TurnClock) Wire() api.TurnClock {
	out := api.TurnClock{
		SessionID:        c.SessionID,
		OpeningMessageID: c.OpeningMessageID,
		ActiveMs:         c.ActiveMs,
		WorkMs:           c.WorkMs,
		Running:          c.RunningAt != nil,
	}
	if c.RunningAt != nil {
		out.RunningAt = c.RunningAt.UTC().Format(time.RFC3339Nano)
	}
	if c.SettledAt != nil {
		out.SettledAt = c.SettledAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// PutTurnClock records a visible user turn's clock. A clock whose opening
// message is not yet in the transcript is not recorded; the next edge after
// the message lands records it.
func (t *sqlTurnTelemetry) PutTurnClock(ctx context.Context, clock TurnClock) error {
	clock = normalizeTurnClock(clock)
	if clock.SessionID == "" || clock.OpeningMessageID == "" {
		return fmt.Errorf("turn clock requires a session and opening message")
	}
	_, err := t.core.queries.UpsertTurnClock(ctx, db.UpsertTurnClockParams{
		OpeningMessageID: clock.OpeningMessageID,
		SessionID:        clock.SessionID,
		ActiveMs:         clock.ActiveMs,
		WorkMs:           clock.WorkMs,
		RunningAt:        nullTime(clock.RunningAt),
		SettledAt:        nullTime(clock.SettledAt),
	})
	return err
}

// LatestTurnClock returns the clock of the session's newest visible user turn.
func (t *sqlTurnTelemetry) LatestTurnClock(ctx context.Context, sessionID string) (TurnClock, bool, error) {
	row, err := t.core.queries.GetLatestTurnClock(ctx, sessionID)
	if db.IsNoRows(err) {
		return TurnClock{}, false, nil
	}
	if err != nil {
		return TurnClock{}, false, err
	}
	clock, err := turnClockFromDB(row)
	return clock, err == nil, err
}

// SettleAbandonedTurnClocks pauses clocks left running by process loss.
func (t *sqlTurnTelemetry) SettleAbandonedTurnClocks(ctx context.Context) (int, error) {
	s := t.core
	settled := 0
	for {
		rows, err := s.queries.ListRunningTurnClocks(ctx)
		if err != nil {
			return settled, err
		}
		for _, row := range rows {
			if err := settleAbandonedTurnClock(ctx, s, row); err != nil {
				return settled, err
			}
			settled++
		}
		if len(rows) < abandonedTurnClockPageSize {
			return settled, nil
		}
	}
}

// LatestTurnProgress is when any of the session's turns last did durable work.
func (t *sqlTurnTelemetry) LatestTurnProgress(ctx context.Context, sessionID string) (time.Time, error) {
	return latestTurnProgressSQL(ctx, t.core, sessionID)
}

func latestTurnProgressSQL(ctx context.Context, s *sqlCore, sessionID string) (time.Time, error) {
	latest, err := s.queries.GetLatestTurnProgressForSession(ctx, sessionID)
	if err != nil {
		return time.Time{}, err
	}
	if latest == "" {
		return time.Time{}, nil
	}
	at, err := db.ParseTime(latest)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse latest turn progress for %s: %w", sessionID, err)
	}
	return at, nil
}

func settleAbandonedTurnClock(ctx context.Context, s *sqlCore, row db.TurnClocks) error {
	clock, err := turnClockFromDB(row)
	if err != nil {
		return err
	}
	lastProgress, err := latestTurnProgressSQL(ctx, s, clock.SessionID)
	if err != nil {
		return err
	}
	clock = clock.settleAbandoned(lastProgress)
	rows, err := s.queries.SettleTurnClock(ctx, db.SettleTurnClockParams{
		ActiveMs:         clock.ActiveMs,
		WorkMs:           clock.WorkMs,
		SettledAt:        nullTime(clock.SettledAt),
		OpeningMessageID: clock.OpeningMessageID,
		RunningAt:        row.RunningAt,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("turn clock changed during recovery: %s", clock.OpeningMessageID)
	}
	return nil
}

func listTurnClocksForPage(ctx context.Context, q *db.Queries, sessionID string, minOrd, maxOrd int64) ([]TurnClock, error) {
	rows, err := q.ListTurnClocksInOrdRange(ctx, db.ListTurnClocksInOrdRangeParams{
		SessionID: sessionID, MinOrd: minOrd, MaxOrd: maxOrd,
	})
	if err != nil {
		return nil, err
	}
	clocks := make([]TurnClock, 0, len(rows))
	for _, row := range rows {
		clock, err := turnClockFromDB(row)
		if err != nil {
			return nil, err
		}
		clocks = append(clocks, clock)
	}
	return clocks, nil
}

func turnClockFromDB(row db.TurnClocks) (TurnClock, error) {
	runningAt, err := parseNullTime(row.RunningAt)
	if err != nil {
		return TurnClock{}, fmt.Errorf("parse turn clock running_at: %w", err)
	}
	settledAt, err := parseNullTime(row.SettledAt)
	if err != nil {
		return TurnClock{}, fmt.Errorf("parse turn clock settled_at: %w", err)
	}
	return TurnClock{
		SessionID:        row.SessionID,
		OpeningMessageID: row.OpeningMessageID,
		ActiveMs:         row.ActiveMs,
		WorkMs:           row.WorkMs,
		RunningAt:        runningAt,
		SettledAt:        settledAt,
	}, nil
}

func nullTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return db.NullString(db.FormatTime(t.UTC()))
}

func parseNullTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := db.ParseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
