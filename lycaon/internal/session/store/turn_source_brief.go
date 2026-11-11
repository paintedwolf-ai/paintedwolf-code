package store

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
)

// PutTurnSourceBrief records the source-change brief a visible user turn opened
// with and the evidence its rows cite, together. The first record wins: a turn's
// brief never changes, and a lost race or absent turn mints nothing.
func (t *sqlTurnTelemetry) PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID string, mint evidence.SourceChangeMint) error {
	s := t.core
	sessionID, openingMessageID, ok, err := turnSourceBriefTarget(ctx, sessionID, openingMessageID, mint)
	if !ok {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeTurnSourceBrief(ctx, s.queries.WithTx(tx), s.dataDir, sessionID, openingMessageID, mint); err != nil {
		return err
	}
	return tx.Commit()
}

// turnSourceBriefTarget trims the brief's address and reports whether it can take one.
func turnSourceBriefTarget(ctx context.Context, sessionID, openingMessageID string, mint evidence.SourceChangeMint) (string, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", "", false, err
	}
	sessionID, openingMessageID = strings.TrimSpace(sessionID), strings.TrimSpace(openingMessageID)
	return sessionID, openingMessageID, sessionID != "" && openingMessageID != "" && mint != nil, nil
}

// writeTurnSourceBrief mints inside the caller's transaction, so a row's handle
// and its record commit or fail together.
func writeTurnSourceBrief(ctx context.Context, q *db.Queries, dataDir, sessionID, openingMessageID string, mint evidence.SourceChangeMint) error {
	projectID, err := q.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("resolve source brief project: %w", err)
	}
	first, err := nextEvidenceOrdinal(ctx, q, sessionID, evidence.KindSourceChange)
	if err != nil {
		return err
	}
	briefJSON, records, err := mint(first)
	if err != nil || strings.TrimSpace(briefJSON) == "" {
		return err
	}
	recorded, err := q.SetSessionSourceTurnBrief(ctx, db.SetSessionSourceTurnBriefParams{
		SourceChangeBrief: db.NullString(briefJSON),
		SessionID:         sessionID,
		OpeningMessageID:  openingMessageID,
	})
	if err != nil || recorded == 0 {
		return err
	}
	for _, rec := range records {
		if _, err := writeEvidenceRecord(ctx, q, dataDir, sessionID, projectID, rec); err != nil {
			return err
		}
	}
	return nil
}

// TurnSourceBriefs returns the session's recorded briefs keyed by the
// message that opened each turn.
func (t *sqlTurnTelemetry) TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	rows, err := t.core.queries.ListSessionSourceTurnBriefs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.OpeningMessageID] = row.SourceChangeBrief
	}
	return out, nil
}

// PutTurnSourceBrief records the source-change brief a visible user turn opened
// with and the evidence its rows cite. The first record wins.
func (t *memoryTurnTelemetry) PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID string, mint evidence.SourceChangeMint) error {
	s := t.core
	sessionID, openingMessageID, ok, err := turnSourceBriefTarget(ctx, sessionID, openingMessageID, mint)
	if !ok {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, set := s.turnSourceBriefs[sessionID][openingMessageID]; set || s.userTurns[sessionID][openingMessageID] == 0 {
		return nil
	}
	briefJSON, records, err := mint(nextEvidenceOrdinalLocked(s, sessionID, evidence.KindSourceChange))
	if err != nil || strings.TrimSpace(briefJSON) == "" {
		return err
	}
	if s.turnSourceBriefs[sessionID] == nil {
		s.turnSourceBriefs[sessionID] = make(map[string]string)
	}
	s.turnSourceBriefs[sessionID][openingMessageID] = briefJSON
	for _, rec := range records {
		ensureEvidenceSession(s, sessionID)[rec.Handle] = memoryEvidenceRow{rec: rec}
	}
	return nil
}

// TurnSourceBriefs returns the session's recorded briefs keyed by the
// message that opened each turn.
func (t *memoryTurnTelemetry) TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error) {
	s := t.core
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.turnSourceBriefs[strings.TrimSpace(sessionID)]), nil
}
