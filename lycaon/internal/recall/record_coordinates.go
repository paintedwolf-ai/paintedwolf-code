package recall

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lycaon/lycaon/internal/search"
)

type recordReference struct {
	request    *HistoryRequest
	observedAt string
}

func (s *Service) recordCoordinates(ctx context.Context, hit search.Hit) (*recordReference, error) {
	if hit.SessionID == "" || hit.MessageID == "" {
		return nil, nil
	}
	var id, observedAt string
	var ord, seq int64
	var err error
	if hit.Source == search.SourceMessage && hit.HitKind == search.HitKindEvidence && hit.Handle != "" {
		// Grounding rows name the citing message, not the original observation.
		err = s.db.QueryRowContext(ctx, `SELECT id, ord, seq, ts FROM messages
            WHERE session_id = ? AND role = 'tool' AND `+historyVisible+`
            AND id = (SELECT message_id FROM evidence_index
                WHERE session_id = ? AND handle = ? AND source = 'tool' AND hit_kind = 'tool'
                AND shape = 'raw' AND tombstoned = 0 LIMIT 1)`,
			hit.SessionID, hit.SessionID, hit.Handle).Scan(&id, &ord, &seq, &observedAt)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT id, ord, seq, ts FROM messages WHERE session_id = ? AND id = ? AND `+historyVisible,
			hit.SessionID, hit.MessageID).Scan(&id, &ord, &seq, &observedAt)
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
	return &recordReference{request: &HistoryRequest{ThroughOrd: ord, MessageID: id, SourceSeq: seq, Format: "record"}, observedAt: observedAt}, nil
}
