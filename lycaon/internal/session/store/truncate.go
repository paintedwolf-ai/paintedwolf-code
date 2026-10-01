package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// TruncateMessagesFrom removes the anchor and later transcript rows.
func (s *SQL) TruncateMessagesFrom(ctx context.Context, sessionID, anchorMessageID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	anchorMessageID = strings.TrimSpace(anchorMessageID)
	if sessionID == "" || anchorMessageID == "" {
		return 0, ErrSessionNotFound
	}
	unlock := s.lockMutation(sessionID)
	defer unlock()
	if _, err := s.Get(ctx, sessionID); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	removed, sess, spillRefs, err := s.truncateMessagesTx(ctx, tx, sessionID, anchorMessageID)
	if err != nil {
		return 0, err
	}
	if err := s.enqueueSessionEvent(ctx, tx, sess, api.SessionEventActionUpdated); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.reclaimUnreferencedSpills(ctx, spillRefs)
	s.outbox.Notify()
	return int(removed), nil
}

func (s *SQL) truncateMessagesTx(ctx context.Context, tx *sql.Tx, sessionID, anchorMessageID string) (int64, *api.Session, []messageSpillRef, error) {
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

// TruncateMessagesFrom removes anchorMessageID and every later row.
func (s *Memory) TruncateMessagesFrom(ctx context.Context, sessionID, anchorMessageID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	anchorMessageID = strings.TrimSpace(anchorMessageID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return 0, ErrSessionNotFound
	}
	msgs := s.messages[sessionID]
	for i := range msgs {
		if msgs[i].ID != anchorMessageID {
			continue
		}
		removedMessages := msgs[i:]
		removed := len(removedMessages)
		removedIDs := make(map[string]struct{}, removed)
		for _, message := range removedMessages {
			removedIDs[message.ID] = struct{}{}
		}
		for openingID := range s.turnClocks[sessionID] {
			if _, ok := removedIDs[openingID]; ok {
				delete(s.turnClocks[sessionID], openingID)
			}
		}
		for submissionID, turnID := range s.turnSubmissions {
			if _, ok := removedIDs[submissionID]; ok {
				s.deleteTurnLocked(turnID)
			}
		}
		for outputID, output := range s.modelOutputs {
			if _, ok := removedIDs[output.MessageID]; !ok {
				continue
			}
			delete(s.modelOutputs, outputID)

			delete(s.liveModelOutputs, outputID)
			delete(s.projectedModelOutputs, outputID)
			for turnID, attempts := range s.turnAttempts {
				for _, attempt := range attempts {
					if attempt.ID == output.TurnAttemptID {
						s.deleteTurnLocked(turnID)
					}
				}
			}
		}
		s.messages[sessionID] = append([]api.Message(nil), msgs[:i]...)
		return removed, nil
	}
	return 0, ErrMessageNotFound
}

func (s *Memory) deleteTurnLocked(turnID string) {
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
