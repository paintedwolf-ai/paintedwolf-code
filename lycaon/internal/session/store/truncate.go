package store

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func truncateMessagesTx(ctx context.Context, s *sqlCore, tx *sql.Tx, sessionID, anchorMessageID string) (int64, *api.Session, []messageSpillRef, error) {
	qtx := s.queries.WithTx(tx)
	anchorOrd, err := qtx.GetSessionMessageOrd(ctx, db.GetSessionMessageOrdParams{SessionID: sessionID, ID: anchorMessageID})
	if err != nil {
		return 0, nil, nil, ErrMessageNotFound
	}
	spillRows, err := qtx.ListSessionSpillRefsFromOrd(ctx, db.ListSessionSpillRefsFromOrdParams{SessionID: sessionID, Ord: anchorOrd})
	if err != nil {
		return 0, nil, nil, err
	}
	spillRefs := spillRefsFromOrdRows(spillRows)
	compactionRows, err := qtx.ListSessionCompactionSpillRefs(ctx, sessionID)
	if err != nil {
		return 0, nil, nil, err
	}
	spillRefs = append(spillRefs, spillRefsFromCompactionRows(compactionRows)...)
	if err := qtx.DeleteCompactionSpillRefs(ctx, sessionID); err != nil {
		return 0, nil, nil, err
	}
	if err := qtx.DeleteCompactionView(ctx, sessionID); err != nil {
		return 0, nil, nil, err
	}
	if err := qtx.DeleteEvidenceIndexFromMessageOrd(ctx, db.DeleteEvidenceIndexFromMessageOrdParams{
		SessionID: sql.NullString{String: sessionID, Valid: true}, SessionID_2: sessionID, Ord: anchorOrd,
	}); err != nil {
		return 0, nil, nil, err
	}
	if err := qtx.DeleteDraftVersionsFromMessageOrd(ctx, db.DeleteDraftVersionsFromMessageOrdParams{
		SessionID: sessionID, SessionID_2: sessionID, Ord: anchorOrd,
	}); err != nil {
		return 0, nil, nil, err
	}
	if err := qtx.DeleteTurnsFromSessionOrd(ctx, db.DeleteTurnsFromSessionOrdParams{
		TargetSessionID: sessionID, Ord: anchorOrd,
	}); err != nil {
		return 0, nil, nil, err
	}
	removed, err := qtx.DeleteSessionEntriesFromOrd(ctx, db.DeleteSessionEntriesFromOrdParams{SessionID: sessionID, Ord: anchorOrd})
	if err != nil {
		return 0, nil, nil, err
	}
	row, err := qtx.GetSession(ctx, sessionID)
	if err != nil {
		return 0, nil, nil, err
	}
	sess, err := sessionFromRow(row)
	return removed, sess, spillRefs, err
}

func deleteTurnLocked(s *memoryCore, turnID string) {
	for _, attempt := range s.turnAttempts[turnID] {
		delete(s.turnCloseouts, attempt.ID)
	}
	delete(s.turns, turnID)
	delete(s.turnAttempts, turnID)
	for submissionID, boundTurnID := range s.turnSubmissions {
		if boundTurnID == turnID {
			delete(s.turnSubmissions, submissionID)
		}
	}
	for workerJobID, turnIDs := range s.workerTurns {
		kept := turnIDs[:0]
		for _, boundTurnID := range turnIDs {
			if boundTurnID != turnID {
				kept = append(kept, boundTurnID)
			}
		}
		if len(kept) == 0 {
			delete(s.workerTurns, workerJobID)
		} else {
			s.workerTurns[workerJobID] = kept
		}
	}
}
