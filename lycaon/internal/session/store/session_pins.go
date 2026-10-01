package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// A project's pinned chats carry pin_rank, an order key unique within the
// project. Pinning appends; moving renumbers the project's pins 1…n. Unpinning,
// archiving, and deletion leave gaps, which order the same way.

// PinSession appends a chat to its project's pinned order; pinning a pinned
// chat changes nothing.
func (s *SQL) PinSession(ctx context.Context, id string) error {
	return s.writePins(ctx, id, func(ctx context.Context, qtx *db.Queries, state db.GetSessionPinStateRow) ([]string, error) {
		if err := pinnable(state); err != nil {
			return nil, err
		}
		if state.PinRank.Valid {
			return nil, nil
		}
		rank, err := qtx.NextSessionPinRank(ctx, state.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := qtx.SetSessionPinRank(ctx, db.SetSessionPinRankParams{
			PinRank: sql.NullInt64{Int64: rank, Valid: true}, UpdatedAt: db.FormatTime(time.Now().UTC()), ID: id,
		}); err != nil {
			return nil, err
		}
		return []string{id}, nil
	})
}

// UnpinSession removes a chat from its project's pinned order.
func (s *SQL) UnpinSession(ctx context.Context, id string) error {
	return s.writePins(ctx, id, func(ctx context.Context, qtx *db.Queries, state db.GetSessionPinStateRow) ([]string, error) {
		if !state.PinRank.Valid {
			return nil, nil
		}
		if err := qtx.SetSessionPinRank(ctx, db.SetSessionPinRankParams{
			UpdatedAt: db.FormatTime(time.Now().UTC()), ID: id,
		}); err != nil {
			return nil, err
		}
		return []string{id}, nil
	})
}

// MovePinnedSession places a pinned chat at a 1-based position in its
// project's pinned order and renumbers the project's pins 1…n.
func (s *SQL) MovePinnedSession(ctx context.Context, id string, position int) error {
	return s.writePins(ctx, id, func(ctx context.Context, qtx *db.Queries, state db.GetSessionPinStateRow) ([]string, error) {
		if !state.PinRank.Valid {
			return nil, ErrSessionNotPinned
		}
		rows, err := qtx.ListPinnedSessions(ctx, state.ProjectID)
		if err != nil {
			return nil, err
		}
		current := make([]pinnedRank, 0, len(rows))
		for _, row := range rows {
			current = append(current, pinnedRank{id: row.ID, rank: row.PinRank, updatedAt: row.UpdatedAt})
		}
		next := movedPinOrder(current, id, position)
		if err := qtx.ClearProjectPinRanks(ctx, state.ProjectID); err != nil {
			return nil, err
		}
		now := db.FormatTime(time.Now().UTC())
		changed := []string{}
		for i, pin := range next {
			rank := int64(i + 1)
			updatedAt := pin.updatedAt
			if rank != pin.rank {
				updatedAt = now
				changed = append(changed, pin.id)
			}
			if err := qtx.SetSessionPinRank(ctx, db.SetSessionPinRankParams{
				PinRank: sql.NullInt64{Int64: rank, Valid: true}, UpdatedAt: updatedAt, ID: pin.id,
			}); err != nil {
				return nil, err
			}
		}
		return changed, nil
	})
}

// writePins runs one pin change and announces every chat whose rank moved.
func (s *SQL) writePins(
	ctx context.Context,
	id string,
	change func(context.Context, *db.Queries, db.GetSessionPinStateRow) ([]string, error),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	unlock := s.lockMutation(id)
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	state, err := qtx.GetSessionPinState(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	changed, err := change(ctx, qtx, state)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		return nil
	}
	for _, changedID := range changed {
		row, err := qtx.GetSession(ctx, changedID)
		if err != nil {
			return err
		}
		sess, err := sessionFromRow(row)
		if err != nil {
			return err
		}
		if err := s.enqueueSessionEvent(ctx, tx, sess, api.SessionEventActionUpdated); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.outbox.Notify()
	return nil
}

func pinnable(state db.GetSessionPinStateRow) error {
	if strings.TrimSpace(state.ParentSessionID.String) != "" {
		return ErrWorkerChildPin
	}
	if state.ArchivedAt.Valid {
		return ErrSessionArchived
	}
	return nil
}

type pinnedRank struct {
	id        string
	rank      int64
	updatedAt string
}

// movedPinOrder returns the pinned order with id at the 1-based position; a
// position past the end places it last.
func movedPinOrder(current []pinnedRank, id string, position int) []pinnedRank {
	next := make([]pinnedRank, 0, len(current))
	var moved *pinnedRank
	for i := range current {
		if current[i].id == id {
			moved = &current[i]
			continue
		}
		next = append(next, current[i])
	}
	if moved == nil {
		return next
	}
	at := position - 1
	if at < 0 {
		at = 0
	}
	if at > len(next) {
		at = len(next)
	}
	next = append(next, pinnedRank{})
	copy(next[at+1:], next[at:])
	next[at] = *moved
	return next
}

// PinSession matches SQL pin ordering in memory.
func (s *Memory) PinSession(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[strings.TrimSpace(id)]
	if !ok {
		return ErrSessionNotFound
	}
	if sess.IsWorkerChild() {
		return ErrWorkerChildPin
	}
	if sess.ArchivedAt != nil {
		return ErrSessionArchived
	}
	if sess.PinRank != nil {
		return nil
	}
	rank := 1
	for _, other := range s.sessions {
		if other.ProjectID == sess.ProjectID && other.PinRank != nil && *other.PinRank >= rank {
			rank = *other.PinRank + 1
		}
	}
	sess.PinRank = &rank
	sess.UpdatedAt = time.Now().UTC()
	return nil
}

// UnpinSession matches SQL pin ordering in memory.
func (s *Memory) UnpinSession(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[strings.TrimSpace(id)]
	if !ok {
		return ErrSessionNotFound
	}
	if sess.PinRank == nil {
		return nil
	}
	sess.PinRank = nil
	sess.UpdatedAt = time.Now().UTC()
	return nil
}

// MovePinnedSession matches SQL pin ordering in memory.
func (s *Memory) MovePinnedSession(ctx context.Context, id string, position int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[strings.TrimSpace(id)]
	if !ok {
		return ErrSessionNotFound
	}
	if sess.PinRank == nil {
		return ErrSessionNotPinned
	}
	current := []pinnedRank{}
	for otherID, other := range s.sessions {
		if other.ProjectID == sess.ProjectID && other.PinRank != nil {
			current = append(current, pinnedRank{id: otherID, rank: int64(*other.PinRank)})
		}
	}
	sort.Slice(current, func(i, j int) bool {
		if current[i].rank != current[j].rank {
			return current[i].rank < current[j].rank
		}
		return current[i].id < current[j].id
	})
	now := time.Now().UTC()
	for i, pin := range movedPinOrder(current, sess.ID, position) {
		rank := i + 1
		if int64(rank) == pin.rank {
			continue
		}
		other := s.sessions[pin.id]
		other.PinRank = &rank
		other.UpdatedAt = now
	}
	return nil
}
