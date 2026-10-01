package store

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// PutTurnSourceBrief records the source-change brief a visible user turn
// opened with. The first record wins: a turn's brief never changes.
func (s *SQL) PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID, briefJSON string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID, openingMessageID = strings.TrimSpace(sessionID), strings.TrimSpace(openingMessageID)
	if sessionID == "" || openingMessageID == "" || strings.TrimSpace(briefJSON) == "" {
		return nil
	}
	return s.queries.SetSessionSourceTurnBrief(ctx, db.SetSessionSourceTurnBriefParams{
		SourceChangeBrief: db.NullString(briefJSON),
		SessionID:         sessionID,
		OpeningMessageID:  openingMessageID,
	})
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

// PutTurnSourceBrief records the source-change brief a visible user turn
// opened with. The first record wins: a turn's brief never changes.
func (s *Memory) PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID, briefJSON string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID, openingMessageID = strings.TrimSpace(sessionID), strings.TrimSpace(openingMessageID)
	if sessionID == "" || openingMessageID == "" || strings.TrimSpace(briefJSON) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userTurns[sessionID][openingMessageID] == 0 {
		return nil
	}
	if s.turnSourceBriefs[sessionID] == nil {
		s.turnSourceBriefs[sessionID] = make(map[string]string)
	}
	if _, set := s.turnSourceBriefs[sessionID][openingMessageID]; !set {
		s.turnSourceBriefs[sessionID][openingMessageID] = briefJSON
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
