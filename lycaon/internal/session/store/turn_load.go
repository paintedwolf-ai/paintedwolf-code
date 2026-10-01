package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// Turn load receipt triggers.
const (
	// TurnLoadTriggerTurn is the decision at a visible user turn or a worker leg start.
	TurnLoadTriggerTurn = "turn"
	// TurnLoadTriggerRequest is a request_tools resolution.
	TurnLoadTriggerRequest = "request"
	// TurnLoadTriggerLookup is a skills_read lookup by description.
	TurnLoadTriggerLookup = "lookup"
	// TurnLoadTriggerToolEvent is the skill read a turn's first loadable tool call asked for.
	TurnLoadTriggerToolEvent = "tool_event"
)

// TurnLoadReceipt records one decision the local decision model was asked.
type TurnLoadReceipt struct {
	ID        int64
	SessionID string
	// OpeningMessageID is the user message that opened the turn the decision
	// belongs to; empty when the turn has no stored opening message.
	OpeningMessageID string
	// ToolCallID is the model call that asked: the request_tools or
	// skills_read call, or the loadable tool call that triggered a skill
	// read; empty for a turn decision.
	ToolCallID string
	Trigger    string
	SurfaceID  string
	// Engine labels the model that answered, or is empty when it abstained.
	Engine string
	// CatalogRevision identifies the unit descriptions the engine scored.
	CatalogRevision string
	// StateJSON is the state the engine read; Decisions its answers and what
	// they established; Standing the session's standing surface afterwards.
	StateJSON string
	Decisions string
	Standing  string
	ElapsedMs int64
	Abstained bool
	Reason    string
	CreatedAt time.Time
}

func (r *TurnLoadReceipt) normalize() error {
	r.SessionID = strings.TrimSpace(r.SessionID)
	if r.SessionID == "" {
		return fmt.Errorf("turn load receipt requires a session")
	}
	if strings.TrimSpace(r.Trigger) == "" {
		return fmt.Errorf("turn load receipt requires a trigger")
	}
	r.OpeningMessageID = strings.TrimSpace(r.OpeningMessageID)
	r.ToolCallID = strings.TrimSpace(r.ToolCallID)
	if strings.TrimSpace(r.Standing) == "" {
		r.Standing = "{}"
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	return nil
}

// PutTurnLoadReceipt appends one receipt and returns it with its id.
func (s *SQL) PutTurnLoadReceipt(ctx context.Context, r TurnLoadReceipt) (TurnLoadReceipt, error) {
	if err := r.normalize(); err != nil {
		return TurnLoadReceipt{}, err
	}
	abstained := int64(0)
	if r.Abstained {
		abstained = 1
	}
	// RETURNING reads through QueryRow, which the read handle would route to
	// a reader; the insert runs on the writer inside its own transaction.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TurnLoadReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := s.queries.WithTx(tx).InsertTurnLoadReceipt(ctx, db.InsertTurnLoadReceiptParams{
		SessionID:        r.SessionID,
		OpeningMessageID: db.NullString(r.OpeningMessageID),
		ToolCallID:       r.ToolCallID,
		Trigger:          r.Trigger,
		SurfaceID:        r.SurfaceID,
		Engine:           r.Engine,
		CatalogRevision:  r.CatalogRevision,
		StateJson:        r.StateJSON,
		DecisionsJson:    r.Decisions,
		StandingJson:     r.Standing,
		ElapsedMs:        max(r.ElapsedMs, 0),
		Abstained:        abstained,
		Reason:           r.Reason,
		CreatedAt:        r.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return TurnLoadReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return TurnLoadReceipt{}, err
	}
	r.ID = id
	return r, nil
}

// ListTurnLoadReceiptsForTurns returns the receipts of the turns the given
// user messages opened, in the order they were recorded.
func (s *SQL) ListTurnLoadReceiptsForTurns(ctx context.Context, openingMessageIDs []string) ([]TurnLoadReceipt, error) {
	ids := trimmedIDs(openingMessageIDs)
	if len(ids) == 0 {
		return []TurnLoadReceipt{}, nil
	}
	keys := make([]sql.NullString, len(ids))
	for i, id := range ids {
		keys[i] = db.NullString(id)
	}
	rows, err := s.queries.ListTurnLoadReceiptsForTurns(ctx, keys)
	if err != nil {
		return nil, err
	}
	return turnLoadReceipts(rows), nil
}

// LatestTurnLoadReceipt returns the session's most recent receipt, whose
// loads are the chat's loaded set when it was recorded.
func (s *SQL) LatestTurnLoadReceipt(ctx context.Context, sessionID string) (TurnLoadReceipt, bool, error) {
	rows, err := s.queries.LatestTurnLoadReceiptForSession(ctx, strings.TrimSpace(sessionID))
	if err != nil || len(rows) == 0 {
		return TurnLoadReceipt{}, false, err
	}
	return turnLoadReceipts(rows)[0], true, nil
}

func trimmedIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func turnLoadReceipts(rows []db.TurnLoadReceipts) []TurnLoadReceipt {
	out := make([]TurnLoadReceipt, 0, len(rows))
	for _, row := range rows {
		created, _ := time.Parse(time.RFC3339Nano, row.CreatedAt)
		out = append(out, TurnLoadReceipt{
			ID:               row.ID,
			SessionID:        row.SessionID,
			OpeningMessageID: row.OpeningMessageID.String,
			ToolCallID:       row.ToolCallID,
			Trigger:          row.Trigger,
			SurfaceID:        row.SurfaceID,
			Engine:           row.Engine,
			CatalogRevision:  row.CatalogRevision,
			StateJSON:        row.StateJson,
			Decisions:        row.DecisionsJson,
			Standing:         row.StandingJson,
			ElapsedMs:        row.ElapsedMs,
			Abstained:        row.Abstained == 1,
			Reason:           row.Reason,
			CreatedAt:        created,
		})
	}
	return out
}

// PutTurnLoadReceipt appends one receipt to the in-memory store.
func (s *Memory) PutTurnLoadReceipt(ctx context.Context, r TurnLoadReceipt) (TurnLoadReceipt, error) {
	if err := ctx.Err(); err != nil {
		return TurnLoadReceipt{}, err
	}
	if err := r.normalize(); err != nil {
		return TurnLoadReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnLoadSeq++
	r.ID = s.turnLoadSeq
	s.turnLoads[r.SessionID] = append(s.turnLoads[r.SessionID], r)
	return r, nil
}

// ListTurnLoadReceiptsForTurns returns the receipts of the turns the given
// user messages opened, in the order they were recorded.
func (s *Memory) ListTurnLoadReceiptsForTurns(_ context.Context, openingMessageIDs []string) ([]TurnLoadReceipt, error) {
	wanted := make(map[string]bool, len(openingMessageIDs))
	for _, id := range trimmedIDs(openingMessageIDs) {
		wanted[id] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []TurnLoadReceipt{}
	for _, rows := range s.turnLoads {
		for _, r := range rows {
			if wanted[r.OpeningMessageID] {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LatestTurnLoadReceipt returns the session's most recent receipt.
func (s *Memory) LatestTurnLoadReceipt(_ context.Context, sessionID string) (TurnLoadReceipt, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.turnLoads[strings.TrimSpace(sessionID)]
	if len(rows) == 0 {
		return TurnLoadReceipt{}, false, nil
	}
	return rows[len(rows)-1], true, nil
}
