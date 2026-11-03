package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// GetMessages returns message history for a session.
func (s *sqlTranscript) GetMessages(ctx context.Context, id string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := getSessionSQL(ctx, s.core, id); err != nil {
		return nil, err
	}
	return ReadMessages(ctx, s.core.queries, id)
}

// GetWorkerJobMessages reads one worker run's rows.
func (s *sqlTranscript) GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil, fmt.Errorf("worker job id required")
	}
	if _, err := getSessionSQL(ctx, s.core, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.core.queries.ListWorkerJobMessages(ctx, db.ListWorkerJobMessagesParams{
		SessionID:   sessionID,
		WorkerJobID: db.NullString(workerJobID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.Message, 0, len(rows))
	for _, row := range rows {
		message, mapErr := messageFromRow(db.ListSessionMessagesRow(row))
		if mapErr != nil {
			return nil, mapErr
		}
		out = append(out, message)
	}
	return out, nil
}

// GetMessage reads one transcript projection.
func (s *sqlTranscript) GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	tx, err := s.core.db.BeginReadTx(ctx)
	if err != nil {
		return api.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.core.queries.WithTx(tx)
	position, err := qtx.GetMessageOrdAndTS(ctx, db.GetMessageOrdAndTSParams{
		SessionID: sessionID, ID: messageID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return api.Message{}, ErrMessageNotFound
	}
	if err != nil {
		return api.Message{}, err
	}
	rows, err := qtx.ListSessionMessagesAfterOrd(ctx, db.ListSessionMessagesAfterOrdParams{
		SessionID: sessionID, Ord: position.Ord - 1, Limit: 1,
	})
	if err != nil {
		return api.Message{}, err
	}
	if len(rows) != 1 || rows[0].ID != messageID {
		return api.Message{}, ErrMessageNotFound
	}
	message, err := messageFromRow(db.ListSessionMessagesRow(rows[0]))
	if err != nil {
		return api.Message{}, err
	}
	return message, tx.Commit()
}

// GetTranscriptPage reads a consistent transcript window and watermark.
func (s *sqlTranscript) GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
	return getTranscriptPageSQL(ctx, s.core, id, q)
}

func getTranscriptPageSQL(ctx context.Context, s *sqlCore, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
	if err := ctx.Err(); err != nil {
		return api.SessionTranscriptPage{}, err
	}
	if _, err := s.queries.GetSessionTranscriptSeq(ctx, id); err != nil {
		if db.IsNoRows(err) {
			return api.SessionTranscriptPage{}, ErrSessionNotFound
		}
		return api.SessionTranscriptPage{}, err
	}
	tx, err := s.db.BeginReadTx(ctx)
	if err != nil {
		return api.SessionTranscriptPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	page := api.SessionTranscriptPage{TurnClocks: map[string]api.TurnClock{}, TurnLoads: map[string][]api.TurnLoad{}}
	page.Watermark, err = qtx.GetSessionTranscriptSeq(ctx, id)
	if err != nil {
		return api.SessionTranscriptPage{}, err
	}
	if strings.TrimSpace(q.WorkerID) != "" {
		var bounds db.GetWorkerJobMessageOrdBoundsRow
		page.Messages, bounds, err = loadWorkerTranscriptPage(ctx, qtx, id, q)
		if err != nil {
			return api.SessionTranscriptPage{}, err
		}
		hasBefore, hasAfter := workerWindowHasMore(bounds.MinOrd, bounds.MaxOrd, page.Messages, q)
		page.OrdBounds = transcriptOrdBounds(bounds.MinOrd, bounds.MaxOrd)
		if err := SetTranscriptPageCursors(id, &page, hasBefore, hasAfter, q); err != nil {
			return api.SessionTranscriptPage{}, err
		}
		return page, tx.Commit()
	}
	page.Messages, err = loadSessionTranscriptMessages(ctx, qtx, id, q)
	if err != nil {
		return api.SessionTranscriptPage{}, err
	}
	if n := len(page.Messages); n > 0 {
		clocks, err := listTurnClocksForPage(ctx, qtx, id, page.Messages[0].Ord, page.Messages[n-1].Ord)
		if err != nil {
			return api.SessionTranscriptPage{}, err
		}
		for _, clock := range clocks {
			page.TurnClocks[clock.OpeningMessageID] = clock.Wire()
		}
	}
	hasBefore, hasAfter, err := sqlWindowHasMore(ctx, qtx, id, page.Messages, q)
	if err != nil {
		return api.SessionTranscriptPage{}, err
	}
	bounds, err := qtx.GetSessionMessageOrdBounds(ctx, id)
	if err != nil {
		return api.SessionTranscriptPage{}, err
	}
	page.OrdBounds = transcriptOrdBounds(bounds.MinOrd, bounds.MaxOrd)
	if err := SetTranscriptPageCursors(id, &page, hasBefore, hasAfter, q); err != nil {
		return api.SessionTranscriptPage{}, err
	}
	return page, tx.Commit()
}

func loadSessionTranscriptMessages(
	ctx context.Context,
	qtx *db.Queries,
	id string,
	q api.TranscriptPageQuery,
) ([]api.Message, error) {
	limit := int64(q.EffectiveLimit())
	var listRows []db.ListSessionMessagesRow
	switch {
	case q.Before != nil:
		rows, err := qtx.ListSessionMessagesBeforeOrd(ctx, db.ListSessionMessagesBeforeOrdParams{
			SessionID: id, Ord: *q.Before, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		listRows = make([]db.ListSessionMessagesRow, len(rows))
		for i, r := range rows {
			listRows[i] = db.ListSessionMessagesRow(r)
		}
	case q.After != nil:
		rows, err := qtx.ListSessionMessagesAfterOrd(ctx, db.ListSessionMessagesAfterOrdParams{
			SessionID: id, Ord: *q.After, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		listRows = make([]db.ListSessionMessagesRow, len(rows))
		for i, r := range rows {
			listRows[i] = db.ListSessionMessagesRow(r)
		}
	default:
		rows, err := qtx.ListSessionMessagesTail(ctx, db.ListSessionMessagesTailParams{
			SessionID: id, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		listRows = make([]db.ListSessionMessagesRow, len(rows))
		for i, r := range rows {
			listRows[i] = db.ListSessionMessagesRow(r)
		}
	}
	messages := make([]api.Message, 0, len(listRows))
	for _, r := range listRows {
		msg, err := messageFromRow(r)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if q.After == nil {
		reverseMessages(messages)
	}
	return messages, nil
}

// ReadMessages decodes stored transcript rows without projections or repair.
func ReadMessages(ctx context.Context, q *db.Queries, sessionID string) ([]api.Message, error) {
	rows, err := q.ListSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Message, 0, len(rows))
	for _, r := range rows {
		msg, err := messageFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}
