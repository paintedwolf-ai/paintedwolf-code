package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReadMessageWindow returns a canonical window in ascending creation order.
func (s *sqlTranscript) ReadMessageWindow(ctx context.Context, id string, window messageview.MessageWindow) ([]api.Message, error) {
	if window.Limit <= 0 {
		return []api.Message{}, nil
	}
	if window.After {
		rows, err := s.core.queries.ListSessionMessagesAfterOrd(ctx, db.ListSessionMessagesAfterOrdParams{SessionID: id, Ord: window.AfterOrd, Limit: int64(window.Limit)})
		return decodeMessageRows(rows, err)
	}
	if window.BeforeOrd > 0 {
		rows, err := s.core.queries.ListSessionMessagesBeforeOrd(ctx, db.ListSessionMessagesBeforeOrdParams{SessionID: id, Ord: window.BeforeOrd, Limit: int64(window.Limit)})
		out, err := decodeMessageRows(rows, err)
		reverseMessages(out)
		return out, err
	}
	rows, err := s.core.queries.ListSessionMessagesTail(ctx, db.ListSessionMessagesTailParams{SessionID: id, Limit: int64(window.Limit)})
	out, err := decodeMessageRows(rows, err)
	reverseMessages(out)
	return out, err
}

func decodeMessageRows[T db.ListSessionMessagesAfterOrdRow | db.ListSessionMessagesBeforeOrdRow | db.ListSessionMessagesTailRow](rows []T, err error) ([]api.Message, error) {
	if err != nil {
		return nil, err
	}
	out := make([]api.Message, 0, len(rows))
	for _, row := range rows {
		message, err := messageFromRow(db.ListSessionMessagesRow(row))
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, nil
}

func (s *memoryTranscript) ReadMessageWindow(ctx context.Context, id string, window messageview.MessageWindow) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()
	if _, ok := core.sessions[id]; !ok {
		return nil, ErrSessionNotFound
	}
	if window.Limit <= 0 {
		return []api.Message{}, nil
	}
	rows := core.messages[id]
	end := len(rows)
	if !window.After && window.BeforeOrd > 0 {
		end = sort.Search(len(rows), func(i int) bool { return rows[i].Ord >= window.BeforeOrd })
	}
	start := max(0, end-window.Limit)
	if window.After {
		start = sort.Search(len(rows), func(i int) bool { return rows[i].Ord > window.AfterOrd })
		end = start + min(window.Limit, len(rows)-start)
	}
	return append([]api.Message(nil), rows[start:end]...), nil
}

// ReadSourceTurn resolves a permanent turn identity; zero selects the newest live turn.
func (s *sqlTranscript) ReadSourceTurn(ctx context.Context, id string, number int) (messageview.SourceTurn, error) {
	if number < 0 {
		return messageview.SourceTurn{}, fmt.Errorf("source turn must be nonnegative")
	}
	if number == 0 {
		value, err := s.core.queries.GetSessionUserTurnOrdinal(ctx, id)
		if err != nil {
			return messageview.SourceTurn{}, err
		}
		number = int(value)
	}
	opening, err := s.core.queries.GetUserTurnOpeningMessageID(ctx, db.GetUserTurnOpeningMessageIDParams{SessionID: id, Turn: int64(number)})
	if db.IsNoRows(err) {
		return messageview.SourceTurn{}, nil
	}
	return messageview.SourceTurn{Number: number, OpeningMessageID: opening}, err
}

func (s *memoryTranscript) ReadSourceTurn(ctx context.Context, id string, number int) (messageview.SourceTurn, error) {
	if err := ctx.Err(); err != nil {
		return messageview.SourceTurn{}, err
	}
	if number < 0 {
		return messageview.SourceTurn{}, fmt.Errorf("source turn must be nonnegative")
	}
	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()
	if number == 0 {
		number = currentUserTurnLocked(core, id)
	}
	for opening, turn := range core.userTurns[id] {
		if turn == number {
			return messageview.SourceTurn{Number: number, OpeningMessageID: opening}, nil
		}
	}
	return messageview.SourceTurn{}, nil
}
