package recall

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lycaon/lycaon/internal/search"
)

func (s *Service) recordCoordinates(ctx context.Context, caller Caller, hit search.Hit) (*HistoryRequest, error) {
	if hit.SessionID != caller.SessionID || hit.MessageID == "" {
		return nil, nil
	}
	var id string
	var ord, seq int64
	var err error
	if hit.Source == search.SourceMessage && hit.HitKind == search.HitKindEvidence && hit.Handle != "" {
		// Grounding rows name the citing message, not the original observation.
		err = s.db.QueryRowContext(ctx, `SELECT id, ord, seq FROM messages
            WHERE session_id = ? AND role = 'tool' AND `+historyVisible+`
            AND id = (SELECT message_id FROM evidence_index
                WHERE session_id = ? AND handle = ? AND source = 'tool' AND hit_kind = 'tool'
                AND shape = 'raw' AND tombstoned = 0 LIMIT 1)`,
			caller.SessionID, caller.SessionID, hit.Handle).Scan(&id, &ord, &seq)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT id, ord, seq FROM messages WHERE session_id = ? AND id = ? AND `+historyVisible,
			caller.SessionID, hit.MessageID).Scan(&id, &ord, &seq)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ord <= 0 {
		return nil, nil
	}
	return &HistoryRequest{ThroughOrd: ord, MessageID: id, SourceSeq: seq, Format: "record"}, nil
}
