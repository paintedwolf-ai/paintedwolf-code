package store

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func reverseMessages(msgs []api.Message) {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
}

// transcriptOrdBounds reports the ord span a transcript holds; an empty
// transcript (both bounds zero) has none.
func transcriptOrdBounds(minOrd, maxOrd int64) *api.TranscriptOrdBounds {
	if minOrd <= 0 || maxOrd <= 0 {
		return nil
	}
	return &api.TranscriptOrdBounds{FirstOrd: minOrd, LastOrd: maxOrd}
}

func loadWorkerTranscriptPage(
	ctx context.Context,
	queries *db.Queries,
	sessionID string,
	query api.TranscriptPageQuery,
) ([]api.Message, db.GetWorkerJobMessageOrdBoundsRow, error) {
	workerJobID := db.NullString(strings.TrimSpace(query.WorkerID))
	limit := int64(query.EffectiveLimit())
	var rows []db.ListSessionMessagesRow
	switch {
	case query.Before != nil:
		found, err := queries.ListWorkerJobMessagesBeforeOrd(ctx, db.ListWorkerJobMessagesBeforeOrdParams{
			SessionID: sessionID, WorkerJobID: workerJobID, Ord: *query.Before, Limit: limit,
		})
		if err != nil {
			return nil, db.GetWorkerJobMessageOrdBoundsRow{}, err
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
			return nil, db.GetWorkerJobMessageOrdBoundsRow{}, err
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
			return nil, db.GetWorkerJobMessageOrdBoundsRow{}, err
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
			return nil, db.GetWorkerJobMessageOrdBoundsRow{}, err
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
		return nil, db.GetWorkerJobMessageOrdBoundsRow{}, err
	}
	return messages, bounds, nil
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
