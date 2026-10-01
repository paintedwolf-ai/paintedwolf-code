package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQLDecisionStore persists decisions in the main database.
type SQLDecisionStore struct {
	queries *db.Queries
}

// NewSQLDecisionStore returns a durable decision store.
func NewSQLDecisionStore(database db.Handle) *SQLDecisionStore {
	if database == nil {
		return &SQLDecisionStore{}
	}
	return &SQLDecisionStore{queries: db.New(database)}
}

func (s *SQLDecisionStore) Put(ctx context.Context, decision api.WorkerDecisionRequest) error {
	if s == nil || s.queries == nil {
		return fmt.Errorf("decision store not configured")
	}
	decision, err := normalizeDecision(decision)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	return s.queries.UpsertWorkerDecision(ctx, db.UpsertWorkerDecisionParams{
		ChildSessionID: decision.ChildSessionID, JobID: decision.WorkerID,
		DecisionJson: string(raw), CreatedAt: db.FormatTime(time.Now().UTC()),
	})
}

func (s *SQLDecisionStore) Get(ctx context.Context, childSessionID string) (api.WorkerDecisionRequest, bool, error) {
	if s == nil || s.queries == nil {
		return api.WorkerDecisionRequest{}, false, fmt.Errorf("decision store not configured")
	}
	row, err := s.queries.GetWorkerDecision(ctx, strings.TrimSpace(childSessionID))
	if db.IsNoRows(err) {
		return api.WorkerDecisionRequest{}, false, nil
	}
	if err != nil {
		return api.WorkerDecisionRequest{}, false, err
	}
	return decodeStoredDecision(row.ChildSessionID, row.JobID, row.DecisionJson)
}

func (s *SQLDecisionStore) GetByJob(ctx context.Context, jobID string) (api.WorkerDecisionRequest, bool, error) {
	if s == nil || s.queries == nil {
		return api.WorkerDecisionRequest{}, false, fmt.Errorf("decision store not configured")
	}
	row, err := s.queries.GetWorkerDecisionByJob(ctx, strings.TrimSpace(jobID))
	if db.IsNoRows(err) {
		return api.WorkerDecisionRequest{}, false, nil
	}
	if err != nil {
		return api.WorkerDecisionRequest{}, false, err
	}
	return decodeStoredDecision(row.ChildSessionID, row.JobID, row.DecisionJson)
}

func (s *SQLDecisionStore) Clear(ctx context.Context, childSessionID string) error {
	if s == nil || s.queries == nil {
		return fmt.Errorf("decision store not configured")
	}
	return s.queries.DeleteWorkerDecision(ctx, strings.TrimSpace(childSessionID))
}

func decodeStoredDecision(childSessionID, jobID, raw string) (api.WorkerDecisionRequest, bool, error) {
	var decision api.WorkerDecisionRequest
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return api.WorkerDecisionRequest{}, false, fmt.Errorf("decode decision: %w", err)
	}
	if strings.TrimSpace(decision.WorkerID) != strings.TrimSpace(jobID) {
		return api.WorkerDecisionRequest{}, false, fmt.Errorf("decision job identity mismatch")
	}
	if strings.TrimSpace(decision.ChildSessionID) != strings.TrimSpace(childSessionID) {
		return api.WorkerDecisionRequest{}, false, fmt.Errorf("decision child identity mismatch")
	}
	return decision, true, nil
}
