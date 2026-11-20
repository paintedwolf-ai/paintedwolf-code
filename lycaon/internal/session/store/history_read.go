package store

import (
	"context"
	"fmt"

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
