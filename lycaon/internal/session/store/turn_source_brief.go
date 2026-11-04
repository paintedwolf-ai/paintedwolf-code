package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
)

// PutTurnSourceBrief records the source-change brief a visible user turn opened
// with and the evidence its rows cite, together. The first record wins: a turn's
// brief never changes, and a lost race or absent turn mints nothing.
func (s *SQL) PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID string, mint evidence.SourceChangeMint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID, openingMessageID = strings.TrimSpace(sessionID), strings.TrimSpace(openingMessageID)
	if sessionID == "" || openingMessageID == "" || mint == nil {
		return nil
	}
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	projectID, err := qtx.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("resolve source brief project: %w", err)
	}
	first, err := nextEvidenceOrdinal(ctx, qtx, sessionID, evidence.KindSourceChange)
	if err != nil {
		return err
	}
	briefJSON, records, err := mint(first)
	if err != nil {
		return err
	}
	if strings.TrimSpace(briefJSON) == "" {
		return nil
	}
	recorded, err := qtx.SetSessionSourceTurnBrief(ctx, db.SetSessionSourceTurnBriefParams{
		SourceChangeBrief: db.NullString(briefJSON),
		SessionID:         sessionID,
		OpeningMessageID:  openingMessageID,
	})
	if err != nil {
		return err
	}
	if recorded == 0 {
		return nil
	}
	for _, rec := range records {
		if _, err := writeEvidenceRecord(ctx, qtx, s.dataDir, sessionID, projectID, rec); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TurnSourceBriefs returns the session's recorded briefs keyed by the
// message that opened each turn.
func (s *SQL) TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListSessionSourceTurnBriefs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.OpeningMessageID] = row.SourceChangeBrief
	}
	return out, nil
}

// PutTurnSourceBrief records the source-change brief a visible user turn opened
// with and the evidence its rows cite. The first record wins.
func (s *Memory) PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID string, mint evidence.SourceChangeMint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID, openingMessageID = strings.TrimSpace(sessionID), strings.TrimSpace(openingMessageID)
	if sessionID == "" || openingMessageID == "" || mint == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userTurns[sessionID][openingMessageID] == 0 {
		return nil
	}
	if _, set := s.turnSourceBriefs[sessionID][openingMessageID]; set {
		return nil
	}
	briefJSON, records, err := mint(s.nextEvidenceOrdinalLocked(sessionID, evidence.KindSourceChange))
	if err != nil {
		return err
	}
	if strings.TrimSpace(briefJSON) == "" {
		return nil
	}
	if s.turnSourceBriefs[sessionID] == nil {
		s.turnSourceBriefs[sessionID] = make(map[string]string)
	}
	s.turnSourceBriefs[sessionID][openingMessageID] = briefJSON
	ledger := s.ensureEvidenceSession(sessionID)
	for _, rec := range records {
		ledger[rec.Handle] = memoryEvidenceRow{rec: rec}
	}
	return nil
}

// TurnSourceBriefs returns the session's recorded briefs keyed by the
// message that opened each turn.
func (s *Memory) TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	recorded := s.turnSourceBriefs[strings.TrimSpace(sessionID)]
	if len(recorded) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(recorded))
	for id, brief := range recorded {
		out[id] = brief
	}
	return out, nil
}
