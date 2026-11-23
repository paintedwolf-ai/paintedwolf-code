package progress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
	api "github.com/lycaon/lycaon/pkg/api"
)

// Apply validates and commits a row mutation and result acknowledgements in one transaction.
func (s *SQLStore) Apply(ctx context.Context, sessionID string, mutation Mutation, opts ApplyOptions) (MutationResult, error) {
	result := MutationResult{
		ReconciliationStatus: "not_requested",
		AddedItemIDs:         []string{},
		ClosedItemIDs:        []string{},
		RemovedItemIDs:       []string{},
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	key := normalizeKey(sessionID)
	if key == "" {
		return result, fmt.Errorf("session id required")
	}
	if s == nil || s.transcript == nil {
		return result, fmt.Errorf("progress transactions not configured")
	}
	if err := ValidateMutation(mutation); err != nil {
		return result, err
	}

	var (
		before, after   Snapshot
		edited          rowEditOutcome
		accountedJobIDs []string
	)
	err := s.transcript.AppendMessagesWith(ctx, key, func(tx *sql.Tx) ([]api.Message, error) {
		q := db.New(tx)
		var txErr error
		before, txErr = querySnapshot(ctx, q, key)
		after = before
		if txErr != nil {
			return nil, txErr
		}
		if len(mutation.AcknowledgeResults) > 0 {
			if opts.Reconciler == nil {
				return nil, &MutationRejection{
					Code: "PROGRESS_RESULT_HANDLE_UNKNOWN",
					Data: map[string]any{"unknown_handles": slices.Clone(mutation.AcknowledgeResults)},
				}
			}
			principal := strings.TrimSpace(opts.PrincipalID)
			if principal == "" {
				principal = key
			}
			ack, ackErr := opts.Reconciler.AcknowledgeTx(ctx, tx, key, principal, mutation.AcknowledgeResults)
			if ackErr != nil {
				return nil, ackErr
			}
			if len(ack.UnknownHandles) > 0 {
				return nil, &MutationRejection{
					Code: "PROGRESS_RESULT_HANDLE_UNKNOWN",
					Data: map[string]any{"unknown_handles": ack.UnknownHandles},
				}
			}
			accountedJobIDs = ack.AccountedJobIDs
		}
		edited, txErr = applyRowMutation(before, mutation, opts.Basis)
		if txErr != nil {
			return nil, txErr
		}
		after, txErr = writeRowsTransition(ctx, q, key, before, edited.rows, edited.nextSeq, opts.RunID)
		if txErr != nil {
			return nil, txErr
		}
		return transitionMessages(before, after), nil
	})
	if err != nil {
		var rejection *MutationRejection
		if errors.As(err, &rejection) {
			if opts.Reconciler != nil {
				before.Reconciliation = opts.Reconciler.Reconciliation(key)
				if rejection.Code == "PROGRESS_RESULT_HANDLE_UNKNOWN" {
					rejection.Data["unaccounted_results"] = opts.Reconciler.UnaccountedResults(key)
				}
			}
			result.Snapshot = before
			if rejection.Data == nil {
				rejection.Data = map[string]any{}
			}
			switch rejection.Code {
			case "PROGRESS_ITEM_NOT_FOUND", "PROGRESS_STALE_BASIS", "PROGRESS_RESULT_HANDLE_UNKNOWN":
				rejection.Data["snapshot"] = before
			}
		}
		return result, err
	}

	if len(mutation.AcknowledgeResults) > 0 {
		opts.Reconciler.Release(key, accountedJobIDs)
		result.ReconciliationStatus = "acknowledged"
	}
	if before.DocumentVersion() != after.DocumentVersion() {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: key, Before: before, After: after})
	}
	if opts.Reconciler != nil {
		after.Reconciliation = opts.Reconciler.Reconciliation(key)
	}
	result.Snapshot = after
	result.Updated = !slices.Equal(before.Rows, after.Rows)
	transitions := ClassifyTransitions(before, after)
	result.StateChanged = transitions.Changed
	if len(transitions.ClosedItemIDs) > 0 {
		result.ClosedItemIDs = transitions.ClosedItemIDs
	}
	result.AddedItemIDs = edited.addedIDs
	result.RemovedItemIDs = edited.removedIDs
	return result, nil
}

// BindRun binds unbound progress to runID and resets items on run changes or terminal bootstrap.
func (s *SQLStore) BindRun(ctx context.Context, sessionID, runID string, resetOnTerminal bool) (bool, error) {
	key := normalizeKey(sessionID)
	targetRun := strings.TrimSpace(runID)
	if s == nil || key == "" || targetRun == "" {
		return false, nil
	}
	if s.transcript == nil {
		return false, fmt.Errorf("progress transactions not configured")
	}
	var (
		before, after Snapshot
		refreshed     bool
	)
	err := s.transcript.AppendMessagesWith(ctx, key, func(tx *sql.Tx) ([]api.Message, error) {
		q := db.New(tx)
		var txErr error
		before, txErr = querySnapshot(ctx, q, key)
		after = before
		if txErr != nil {
			return nil, txErr
		}
		refreshed = before.RunID != "" && (before.RunID != targetRun || (resetOnTerminal && before.AllTerminal()))
		nextRows := before.Rows
		if refreshed {
			nextRows = []Row{}
		}
		after, txErr = writeRowsTransition(ctx, q, key, before, nextRows, before.NextItemSeq(), targetRun)
		if txErr != nil {
			return nil, txErr
		}
		return transitionMessages(before, after), nil
	})
	if err != nil {
		return false, err
	}
	if before.DocumentVersion() != after.DocumentVersion() {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: key, Before: before, After: after})
	}
	return refreshed, nil
}

// SeedIfMissing populates initial rows for runID when no rows exist yet.
func (s *SQLStore) SeedIfMissing(ctx context.Context, sessionID, runID string, specs []RowSpec) (Snapshot, error) {
	key := normalizeKey(sessionID)
	targetRun := strings.TrimSpace(runID)
	if key == "" {
		return Snapshot{}, fmt.Errorf("session id required")
	}
	if s == nil || s.transcript == nil {
		return Snapshot{}, fmt.Errorf("progress transactions not configured")
	}
	var before, after Snapshot
	err := s.transcript.AppendMessagesWith(ctx, key, func(tx *sql.Tx) ([]api.Message, error) {
		q := db.New(tx)
		var txErr error
		before, txErr = querySnapshot(ctx, q, key)
		after = before
		if txErr != nil {
			return nil, txErr
		}
		if before.RunID != "" && targetRun != "" && before.RunID != targetRun {
			return nil, nil
		}
		nextRows := before.Rows
		nextSeq := before.NextItemSeq()
		if before.Missing() && len(specs) > 0 {
			nextRows = make([]Row, 0, len(specs))
			for _, spec := range specs {
				label := strings.TrimSpace(spec.Label)
				if label == "" {
					continue
				}
				state := strings.TrimSpace(spec.State)
				if !isValidState(state) {
					state = ProgressStatePending
				}
				id := "p" + strconv.FormatUint(nextSeq, 10)
				nextSeq++
				nextRows = append(nextRows, Row{ID: id, State: state, Label: label})
			}
		}
		after, txErr = writeRowsTransition(ctx, q, key, before, nextRows, nextSeq, targetRun)
		if txErr != nil {
			return nil, txErr
		}
		return transitionMessages(before, after), nil
	})
	if err != nil {
		return before, err
	}
	if before.DocumentVersion() != after.DocumentVersion() {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: key, Before: before, After: after})
	}
	return after, nil
}

// Clear removes visible items while preserving next_item_seq and notifies observers.
func (s *SQLStore) Clear(ctx context.Context, sessionID string) error {
	key := normalizeKey(sessionID)
	if s == nil || s.database == nil || key == "" {
		return nil
	}
	var before, after Snapshot
	err := db.InTx(ctx, s.database, nil, nil, func(q *db.Queries, _ *sql.Tx) error {
		var txErr error
		before, txErr = querySnapshot(ctx, q, key)
		after = before
		if txErr != nil {
			return txErr
		}
		if len(before.Rows) == 0 {
			return nil
		}
		if err := q.DeleteSessionProgressItems(ctx, key); err != nil {
			return err
		}
		if err := q.BumpSessionProgressOnClear(ctx, db.BumpSessionProgressOnClearParams{
			UpdatedAt: timelayout.Format(nextSnapshotTime(before.version)),
			SessionID: key,
		}); err != nil {
			return err
		}
		after, txErr = querySnapshot(ctx, q, key)
		return txErr
	})
	if err != nil {
		return err
	}
	if after.DocumentVersion() > 0 {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: key, Before: before, After: after})
	}
	return nil
}

func writeRowsTransition(ctx context.Context, q *db.Queries, key string, before Snapshot, rows []Row, nextSeq uint64, requestedRunID string) (Snapshot, error) {
	if rows == nil {
		rows = []Row{}
	}
	if nextSeq < 1 {
		nextSeq = 1
	}
	runID := strings.TrimSpace(requestedRunID)
	if runID == "" {
		runID = before.RunID
	}
	if slices.Equal(rows, before.Rows) && nextSeq == before.NextItemSeq() && runID == before.RunID {
		return before, nil
	}
	if runID == "" {
		run, err := q.ActiveWorkflowRunBySession(ctx, key)
		if err != nil {
			return Snapshot{}, fmt.Errorf("resolve progress workflow run: %w", err)
		}
		runID = run.ID
	}
	run, err := q.GetWorkflowRun(ctx, runID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read progress workflow run: %w", err)
	}
	if run.SessionID != key {
		return Snapshot{}, fmt.Errorf("progress workflow run belongs to another session")
	}
	stamp := timelayout.Format(nextSnapshotTime(before.version))
	if err := q.UpsertSessionProgressHeader(ctx, db.UpsertSessionProgressHeaderParams{
		SessionID:     key,
		WorkflowRunID: runID,
		UpdatedAt:     stamp,
		NextItemSeq:   int64(nextSeq), //nolint:gosec // G115 — bounded per-session item sequence.
	}); err != nil {
		return Snapshot{}, err
	}
	if err := q.DeleteSessionProgressItems(ctx, key); err != nil {
		return Snapshot{}, err
	}
	for i, r := range rows {
		if err := q.InsertSessionProgressItem(ctx, db.InsertSessionProgressItemParams{
			SessionID: key,
			ItemID:    r.ID,
			Ord:       int64(i),
			State:     r.State,
			Label:     r.Label,
		}); err != nil {
			return Snapshot{}, err
		}
	}
	return querySnapshot(ctx, q, key)
}
