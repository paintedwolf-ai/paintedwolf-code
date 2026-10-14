package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

type compactionProjection struct {
	Format       int                       `json:"format"`
	Messages     []api.Message             `json:"messages"`
	Continuation *messageview.Continuation `json:"continuation,omitempty"`
}

// PutCompactionView atomically publishes a fresh view and its next generation.
func (s *SQL) PutCompactionView(ctx context.Context, sessionID string, view CompactionView) error {
	if len(view.Messages) > messageview.MaxCompactionRows {
		return fmt.Errorf("compaction projection exceeds its row budget")
	}
	viewJSON, err := json.Marshal(compactionProjection{Format: 1, Messages: view.Messages, Continuation: view.Continuation})
	if err != nil {
		return err
	}
	var oldRefs []messageSpillRef
	previousGeneration := 0
	err = s.updateSession(ctx, sessionID, func(sess *api.Session) {
		previousGeneration = sess.CompactionGeneration
		sess.CompactionGeneration = view.Generation
	}, nil, func(tx *sql.Tx) error {
		if view.Generation != previousGeneration+1 {
			return fmt.Errorf("compaction generation changed")
		}
		qtx := s.queries.WithTx(tx)
		if err := validateCompactionSource(ctx, qtx, sessionID, view); err != nil {
			return err
		}
		projectID, err := qtx.GetSessionProjectID(ctx, sessionID)
		if err != nil {
			return err
		}
		oldRows, err := qtx.ListSessionCompactionSpillRefs(ctx, sessionID)
		if err != nil {
			return err
		}
		oldRefs = spillRefsFromCompactionRows(oldRows)
		if err := qtx.UpsertCompactionView(ctx, db.UpsertCompactionViewParams{
			SessionID:               sessionID,
			Generation:              int64(view.Generation),
			ViewJson:                string(viewJSON),
			CoveredThroughOrd:       view.CoveredThroughOrd,
			CoveredThroughMessageID: db.NullString(view.CoveredThroughID),
			SourceSeq:               view.SourceSeq,
			TokensBefore:            int64(view.TokensBefore),
			TokensAfter:             int64(view.TokensAfter),
			CreatedAt:               db.FormatTime(time.Now().UTC()),
		}); err != nil {
			return err
		}
		if err := qtx.DeleteCompactionSpillRefs(ctx, sessionID); err != nil {
			return err
		}
		if err := writeCompactionSpillRefs(ctx, qtx, sessionID, projectID, view.Messages); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.reclaimUnreferencedSpills(ctx, oldRefs)
	return nil
}

func validateCompactionSource(ctx context.Context, queries *db.Queries, sessionID string, view CompactionView) error {
	clock, err := queries.GetSessionTranscriptSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	if clock < view.SourceSeq || clock-view.SourceSeq > messageview.MaxCompactionRevisionLag {
		return fmt.Errorf("compaction source revision gap exceeded")
	}
	if view.CoveredThroughOrd > 0 {
		boundary, err := queries.GetMessageOrdAndTS(ctx, db.GetMessageOrdAndTSParams{SessionID: sessionID, ID: view.CoveredThroughID})
		if err != nil {
			return err
		}
		if boundary.Ord != view.CoveredThroughOrd {
			return fmt.Errorf("compaction boundary changed")
		}
	}
	mutations, err := queries.CountCompactionSourceMutations(ctx, db.CountCompactionSourceMutationsParams{SessionID: sessionID, Ord: view.CoveredThroughOrd, Seq: view.SourceSeq})
	if err != nil {
		return err
	}
	if mutations != 0 {
		return fmt.Errorf("compaction source changed")
	}
	return nil
}

// GetCompactionView returns the session's compacted view, ok=false when none exists.
func (s *SQL) GetCompactionView(ctx context.Context, sessionID string) (*CompactionView, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	row, err := s.queries.GetCompactionView(ctx, sessionID)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	view := CompactionView{
		Generation:        int(row.Generation),
		CoveredThroughOrd: row.CoveredThroughOrd,
		CoveredThroughID:  db.StringFromNull(row.CoveredThroughMessageID),
		SourceSeq:         row.SourceSeq,
		TokensBefore:      int(row.TokensBefore),
		TokensAfter:       int(row.TokensAfter),
	}
	var projection compactionProjection
	if len(row.ViewJson) > 0 && row.ViewJson[0] == '[' {
		// Canonical history rebuilds projections without a continuation contract.
		return nil, false, nil
	}
	if err := json.Unmarshal([]byte(row.ViewJson), &projection); err != nil {
		return nil, false, err
	}
	if projection.Format != 1 {
		return nil, false, nil
	}
	view.Messages, view.Continuation = projection.Messages, projection.Continuation
	if t, perr := db.ParseTime(row.CreatedAt); perr == nil {
		view.CreatedAt = t
	}
	return &view, true, nil
}

// CompactionViewCurrent detects mutations at or below the view watermark.
func (s *SQL) CompactionViewCurrent(ctx context.Context, sessionID string, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) (bool, error) {
	clock, err := s.queries.GetSessionTranscriptSeq(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if clock < sourceSeq || clock-sourceSeq > messageview.MaxCompactionRevisionLag {
		return false, nil
	}
	if coveredThroughOrd > 0 {
		boundary, err := s.queries.GetMessageOrdAndTS(ctx, db.GetMessageOrdAndTSParams{
			SessionID: sessionID,
			ID:        coveredThroughID,
		})
		if db.IsNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if boundary.Ord != coveredThroughOrd {
			return false, nil
		}
	}
	mutations, err := s.queries.CountCompactionSourceMutations(ctx, db.CountCompactionSourceMutationsParams{
		SessionID: sessionID,
		Ord:       coveredThroughOrd,
		Seq:       sourceSeq,
	})
	return mutations == 0, err
}
