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
func (s *SQL) GetMessages(ctx context.Context, id string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	return ReadMessages(ctx, s.queries, id)
}

// GetWorkerJobMessages reads one worker run's rows.
func (s *SQL) GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil, fmt.Errorf("worker job id required")
	}
	if _, err := s.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListWorkerJobMessages(ctx, db.ListWorkerJobMessagesParams{
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

// GetMessagesAfterOrd seeks through the immutable transcript order.
func (s *SQL) GetMessagesAfterOrd(ctx context.Context, sessionID string, afterOrd int64, limit int) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []api.Message{}, nil
	}
	rows, err := s.queries.ListSessionMessagesAfterOrd(ctx, db.ListSessionMessagesAfterOrdParams{
		SessionID: sessionID,
		Ord:       afterOrd,
		Limit:     int64(limit),
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
func (s *SQL) GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	tx, err := s.db.BeginReadTx(ctx)
	if err != nil {
		return api.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
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
func (s *SQL) GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
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
	limit := int64(q.EffectiveLimit())
	var listRows []db.ListSessionMessagesRow
	switch {
	case strings.TrimSpace(q.WorkerID) != "":
		var hasBefore, hasAfter bool
		page.Messages, hasBefore, hasAfter, err = loadWorkerTranscriptPage(ctx, qtx, id, q)
		if err != nil {
			return api.SessionTranscriptPage{}, err
		}
		if err := SetTranscriptPageCursors(id, &page, hasBefore, hasAfter, q); err != nil {
			return api.SessionTranscriptPage{}, err
		}
		return page, tx.Commit()
	case q.Before != nil:
		rows, qerr := qtx.ListSessionMessagesBeforeOrd(ctx, db.ListSessionMessagesBeforeOrdParams{
			SessionID: id,
			Ord:       *q.Before,
			Limit:     limit,
		})
		if qerr != nil {
			return api.SessionTranscriptPage{}, qerr
		}
		listRows = make([]db.ListSessionMessagesRow, len(rows))
		for i, r := range rows {
			listRows[i] = db.ListSessionMessagesRow(r)
		}
	case q.After != nil:
		rows, qerr := qtx.ListSessionMessagesAfterOrd(ctx, db.ListSessionMessagesAfterOrdParams{
			SessionID: id,
			Ord:       *q.After,
			Limit:     limit,
		})
		if qerr != nil {
			return api.SessionTranscriptPage{}, qerr
		}
		listRows = make([]db.ListSessionMessagesRow, len(rows))
		for i, r := range rows {
			listRows[i] = db.ListSessionMessagesRow(r)
		}
	default:
		rows, qerr := qtx.ListSessionMessagesTail(ctx, db.ListSessionMessagesTailParams{
			SessionID: id,
			Limit:     limit,
		})
		if qerr != nil {
			return api.SessionTranscriptPage{}, qerr
		}
		listRows = make([]db.ListSessionMessagesRow, len(rows))
		for i, r := range rows {
			listRows[i] = db.ListSessionMessagesRow(r)
		}
	}
	page.Messages = make([]api.Message, 0, len(listRows))
	for _, r := range listRows {
		msg, merr := messageFromRow(r)
		if merr != nil {
			return api.SessionTranscriptPage{}, merr
		}
		page.Messages = append(page.Messages, msg)
	}
	// Tail and before queries return DESC — reverse to ascending Ord.
	if q.After == nil {
		reverseMessages(page.Messages)
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
	if err := SetTranscriptPageCursors(id, &page, hasBefore, hasAfter, q); err != nil {
		return api.SessionTranscriptPage{}, err
	}
	return page, tx.Commit()
}

func loadWorkerTranscriptPage(
	ctx context.Context,
	queries *db.Queries,
	sessionID string,
	query api.TranscriptPageQuery,
) ([]api.Message, bool, bool, error) {
	workerJobID := db.NullString(strings.TrimSpace(query.WorkerID))
	limit := int64(query.EffectiveLimit())
	var rows []db.ListSessionMessagesRow
	switch {
	case query.Before != nil:
		found, err := queries.ListWorkerJobMessagesBeforeOrd(ctx, db.ListWorkerJobMessagesBeforeOrdParams{
			SessionID: sessionID, WorkerJobID: workerJobID, Ord: *query.Before, Limit: limit,
		})
		if err != nil {
			return nil, false, false, err
		}
		rows = make([]db.ListSessionMessagesRow, len(found))
		for i, row := range found {
			rows[i] = db.ListSessionMessagesRow(row)
		}
	case query.After != nil:
		found, err := queries.ListWorkerJobMessagesAfterOrd(ctx, db.ListWorkerJobMessagesAfterOrdParams{
			SessionID: sessionID, WorkerJobID: workerJobID, Ord: *query.After, Limit: limit,
		})
		if err != nil {
			return nil, false, false, err
		}
		rows = make([]db.ListSessionMessagesRow, len(found))
		for i, row := range found {
			rows[i] = db.ListSessionMessagesRow(row)
		}
	default:
		found, err := queries.ListWorkerJobMessagesTail(ctx, db.ListWorkerJobMessagesTailParams{
			SessionID: sessionID, WorkerJobID: workerJobID, Limit: limit,
		})
		if err != nil {
			return nil, false, false, err
		}
		rows = make([]db.ListSessionMessagesRow, len(found))
		for i, row := range found {
			rows[i] = db.ListSessionMessagesRow(row)
		}
	}
	messages := make([]api.Message, 0, len(rows))
	for _, row := range rows {
		message, err := messageFromRow(row)
		if err != nil {
			return nil, false, false, err
		}
		messages = append(messages, message)
	}
	if query.After == nil {
		reverseMessages(messages)
	}
	bounds, err := queries.GetWorkerJobMessageOrdBounds(ctx, db.GetWorkerJobMessageOrdBoundsParams{
		SessionID: sessionID, WorkerJobID: workerJobID,
	})
	if err != nil {
		return nil, false, false, err
	}
	hasBefore, hasAfter := workerWindowHasMore(bounds.MinOrd, bounds.MaxOrd, messages, query)
	return messages, hasBefore, hasAfter, nil
}

func workerWindowHasMore(minOrd, maxOrd int64, window []api.Message, query api.TranscriptPageQuery) (before, after bool) {
	if minOrd == 0 || maxOrd == 0 {
		return false, false
	}
	if len(window) > 0 {
		return minOrd < window[0].Ord, maxOrd > window[len(window)-1].Ord
	}
	switch {
	case query.Before != nil:
		return minOrd < *query.Before, maxOrd >= *query.Before
	case query.After != nil:
		return minOrd <= *query.After, maxOrd > *query.After
	default:
		return false, false
	}
}

func sqlExists(v int64) bool { return v != 0 }

func sqlWindowHasMore(ctx context.Context, q *db.Queries, sessionID string, window []api.Message, query api.TranscriptPageQuery) (before, after bool, err error) {
	if len(window) > 0 {
		oldest, newest := window[0].Ord, window[len(window)-1].Ord
		n, err := q.SessionHasMessageBeforeOrd(ctx, db.SessionHasMessageBeforeOrdParams{
			SessionID: sessionID,
			Ord:       oldest,
		})
		if err != nil {
			return false, false, err
		}
		m, err := q.SessionHasMessageAfterOrd(ctx, db.SessionHasMessageAfterOrdParams{
			SessionID: sessionID,
			Ord:       newest,
		})
		return sqlExists(n), sqlExists(m), err
	}
	switch {
	case query.Before != nil:
		n, err := q.SessionHasMessageBeforeOrd(ctx, db.SessionHasMessageBeforeOrdParams{
			SessionID: sessionID,
			Ord:       *query.Before,
		})
		if err != nil {
			return false, false, err
		}
		m, err := q.SessionHasMessageAtOrAfterOrd(ctx, db.SessionHasMessageAtOrAfterOrdParams{
			SessionID: sessionID,
			Ord:       *query.Before,
		})
		return sqlExists(n), sqlExists(m), err
	case query.After != nil:
		m, err := q.SessionHasMessageAfterOrd(ctx, db.SessionHasMessageAfterOrdParams{
			SessionID: sessionID,
			Ord:       *query.After,
		})
		if err != nil {
			return false, false, err
		}
		n, err := q.SessionHasMessageAtOrBeforeOrd(ctx, db.SessionHasMessageAtOrBeforeOrdParams{
			SessionID: sessionID,
			Ord:       *query.After,
		})
		return sqlExists(n), sqlExists(m), err
	default:
		return false, false, nil
	}
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
