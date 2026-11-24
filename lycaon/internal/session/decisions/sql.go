package decisions

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/timelayout"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQL persists decisions in the main database.
type SQL struct {
	queries *db.Queries
	onPut   func(ctx context.Context, decision api.WorkerDecisionRequest)
	onClear func(ctx context.Context, childSessionID string)
}

// NewSQL returns a durable decision store.
func NewSQL(database db.Handle) *SQL {
	if database == nil {
		return &SQL{}
	}
	return &SQL{queries: db.New(database)}
}

// SetWatchHooks attaches post-commit observers for decision creation and clearance.
func (s *SQL) SetWatchHooks(onPut func(ctx context.Context, decision api.WorkerDecisionRequest), onClear func(ctx context.Context, childSessionID string)) {
	if s == nil {
		return
	}
	s.onPut = onPut
	s.onClear = onClear
}

func (s *SQL) Put(ctx context.Context, decision api.WorkerDecisionRequest) error {
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
	if err := s.queries.UpsertWorkerDecision(ctx, db.UpsertWorkerDecisionParams{
		ChildSessionID: decision.ChildSessionID, JobID: decision.WorkerID,
		DecisionJson: string(raw), CreatedAt: timelayout.Format(time.Now().UTC()),
	}); err != nil {
		return err
	}
	if s.onPut != nil {
		s.onPut(ctx, decision)
	}
	return nil
}

func (s *SQL) Get(ctx context.Context, childSessionID string) (api.WorkerDecisionRequest, bool, error) {
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

func (s *SQL) GetByJob(ctx context.Context, jobID string) (api.WorkerDecisionRequest, bool, error) {
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

func (s *SQL) Clear(ctx context.Context, childSessionID string) error {
	if s == nil || s.queries == nil {
		return fmt.Errorf("decision store not configured")
	}
	if err := s.queries.DeleteWorkerDecision(ctx, strings.TrimSpace(childSessionID)); err != nil {
		return err
	}
	if s.onClear != nil {
		s.onClear(ctx, strings.TrimSpace(childSessionID))
	}
	return nil
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
