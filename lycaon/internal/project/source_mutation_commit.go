package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// commit atomically records attribution, events, and completion.
func (s *SourceMutationService) commit(ctx context.Context, row *sourceMutationRow) error {
	if s.db == nil {
		return s.commitWithoutStore(ctx, row)
	}
	delivery, err := s.commitOnce(ctx, row)
	if err != nil {
		return s.noteCommitFailure(ctx, row, err)
	}
	delivery.DeliverCommitted()
	notifySourceMutation(ctx, row.Plan)
	return nil
}

// commitOnce closes its transaction before failure tracking.
func (s *SourceMutationService) commitOnce(ctx context.Context, row *sourceMutationRow) (*sourcefeed.StagedDelivery, error) {
	history, err := s.buildSourceHistoryEntry(ctx, row.ID, &row.Plan)
	if err != nil {
		return nil, err
	}
	var prepared sourceledger.PreparedRecording
	if row.Plan.Changed && s.ledger != nil {
		prepared, err = s.ledger.Prepare(ctx, row.Plan.ledgerInputs(row.ID))
		if err != nil {
			return nil, err
		}
		defer prepared.Close()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	response := append(json.RawMessage(nil), row.Plan.Response...)
	updatedAt := time.Now().UTC()
	delivery, err := commitSourceMutationTx(ctx, tx, row, response, updatedAt, history, prepared)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	row.Status, row.Response, row.Error, row.UpdatedAt = sourceMutationCommitted, response, "", updatedAt
	return delivery, nil
}

func commitSourceMutationTx(ctx context.Context, tx *sql.Tx, row *sourceMutationRow, response json.RawMessage, updatedAt time.Time, history *sourceHistoryEntry, prepared sourceledger.PreparedRecording) (*sourcefeed.StagedDelivery, error) {
	var delivery *sourcefeed.StagedDelivery
	if row.Plan.Changed {
		if prepared != nil {
			if _, err := prepared.CommitTx(ctx, tx); err != nil {
				return nil, err
			}
		}
		var err error
		delivery, err = sourcefeed.EmitBatchTx(ctx, tx, row.Plan.sourceChanges())
		if err != nil {
			return nil, err
		}
	}
	if err := commitSourceHistoryTx(ctx, tx, row.Plan, history, updatedAt); err != nil {
		return nil, err
	}
	completedPlan, err := committedAgentEffectPlan(row.Plan)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_mutations SET status=?, response_json=?, error=?, updated_at=?, plan_json=COALESCE(?,plan_json) WHERE id=?`,
		sourceMutationCommitted, nullableSourceResponse(response), "", updatedAt.Format(time.RFC3339Nano), completedPlan, row.ID)
	return delivery, err
}

// noteCommitFailure leaves the operation recoverable at file_applied.
func (s *SourceMutationService) noteCommitFailure(ctx context.Context, row *sourceMutationRow, cause error) error {
	row.Error = cause.Error()
	if err := s.update(ctx, row); err != nil {
		return errors.Join(cause, fmt.Errorf("record source mutation commit failure: %w", err))
	}
	return cause
}

// commitWithoutStore orders writes for the in-memory service.
func (s *SourceMutationService) commitWithoutStore(ctx context.Context, row *sourceMutationRow) error {
	history, err := s.buildSourceHistoryEntry(ctx, row.ID, &row.Plan)
	if err != nil {
		return err
	}
	if row.Plan.Changed {
		if s.ledger != nil {
			if err := s.ledger.RecordBatch(ctx, row.Plan.ledgerInputs(row.ID)); err != nil {
				return s.noteCommitFailure(ctx, row, err)
			}
		}
		if err := sourcefeed.EmitBatch(ctx, row.Plan.sourceChanges()); err != nil {
			return s.noteCommitFailure(ctx, row, err)
		}
	}
	if err := s.commitSourceHistoryMemory(row.Plan, history); err != nil {
		return s.noteCommitFailure(ctx, row, err)
	}
	row.Status, row.Response, row.Error = sourceMutationCommitted, append(json.RawMessage(nil), row.Plan.Response...), ""
	if err := s.update(ctx, row); err != nil {
		return err
	}
	notifySourceMutation(ctx, row.Plan)
	return nil
}

func notifySourceMutation(ctx context.Context, plan sourceMutationPlan) {
	byRoot := map[string][]string{}
	var collect func(sourceMutationPlan)
	collect = func(current sourceMutationPlan) {
		if current.Kind == "batch_write" {
			for _, write := range current.Writes {
				collect(write)
			}
			return
		}
		if !current.Changed || strings.TrimSpace(current.RootPath) == "" {
			return
		}
		byRoot[current.RootPath] = append(byRoot[current.RootPath], sourceMutationPaths(current)...)
	}
	collect(plan)
	for root, paths := range byRoot {
		repochange.Notify(ctx, repochange.Event{
			ProjectDir: root, Kind: repochange.WorktreeChanged,
			Paths: paths, Source: repochange.SourceMutation,
		})
	}
}

func sourceMutationPaths(plan sourceMutationPlan) []string {
	out := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	for _, candidate := range []string{plan.Path, plan.FromPath, plan.ToPath} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	if len(out) == 0 {
		return []string{"."}
	}
	return out
}
