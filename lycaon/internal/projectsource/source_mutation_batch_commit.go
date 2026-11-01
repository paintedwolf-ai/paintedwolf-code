package projectsource

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

type preparedSettlement struct {
	row           *sourceMutationRow
	history       *sourceHistoryEntry
	recording     sourceledger.PreparedRecording
	response      json.RawMessage
	completedPlan any
	delivery      *sourcefeed.StagedDelivery
}

type batchPreparationRecorder interface {
	PrepareMany(context.Context, [][]sourceledger.RecordInput) ([]sourceledger.PreparedRecording, error)
}

func (s *sourceSettlement) commitRowsOnce(ctx context.Context, rows []*sourceMutationRow) ([]*sourcefeed.StagedDelivery, error) {
	prepared, err := s.prepareRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	defer closePreparedSettlements(prepared)
	return s.commitPreparedRows(ctx, prepared)
}

func closePreparedSettlements(prepared []preparedSettlement) {
	for _, item := range prepared {
		if item.recording != nil {
			item.recording.Close()
		}
	}
}

func (s *sourceSettlement) prepareRows(ctx context.Context, rows []*sourceMutationRow) (out []preparedSettlement, result error) {
	if len(rows) == 0 {
		return nil, nil
	}
	prepared := make([]preparedSettlement, 0, len(rows))
	defer func() {
		if result != nil {
			closePreparedSettlements(prepared)
		}
	}()
	var batchRec batchPreparationRecorder
	if s.recorder != nil {
		batchRec, _ = s.recorder.(batchPreparationRecorder)
	}
	var batchIndices []int
	var batchInputs [][]sourceledger.RecordInput
	for _, row := range rows {
		history, err := s.History.buildSourceHistoryEntry(ctx, row.ID, &row.Plan, row.memory)
		if err != nil {
			return nil, err
		}
		var completedPlan any
		if len(rows) == 1 || row.Plan.AgentEffect == nil || history != nil || row.Plan.HistoryEntryID != "" || string(row.Plan.Response) != "{}" {
			completedPlan, err = committedAgentEffectPlan(row.Plan)
			if err != nil {
				return nil, err
			}
		}
		item := preparedSettlement{
			row:           row,
			history:       history,
			response:      append(json.RawMessage(nil), row.Plan.Response...),
			completedPlan: completedPlan,
		}
		if row.Plan.Changed && s.recorder != nil {
			inputs := row.Plan.ledgerInputs(row.ID)
			if batchRec != nil && len(rows) > 1 {
				batchIndices = append(batchIndices, len(prepared))
				batchInputs = append(batchInputs, inputs)
			} else {
				item.recording, err = s.recorder.Prepare(ctx, inputs)
				if err != nil {
					return nil, err
				}
			}
		}
		prepared = append(prepared, item)
	}
	if len(batchInputs) > 0 {
		recordings, err := batchRec.PrepareMany(ctx, batchInputs)
		if err != nil {
			return nil, err
		}
		for i, idx := range batchIndices {
			prepared[idx].recording = recordings[i]
		}
	}
	return prepared, nil
}

func (s *sourceSettlement) commitPreparedRows(ctx context.Context, prepared []preparedSettlement) ([]*sourcefeed.StagedDelivery, error) {
	if len(prepared) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	updatedAt := time.Now().UTC()
	var cache *sourceledger.TxBatchCache
	if len(prepared) > 1 {
		allFresh := true
		for i := range prepared {
			if !prepared[i].row.freshOperation {
				allFresh = false
				break
			}
		}
		cache = sourceledger.NewTxBatchCache(allFresh)
		cache.BindTx(tx)
	}
	combinedChanges, combineEvents := collectCombinedSourceChanges(prepared)
	quotaBytes := s.History.recoveryQuotaBytes()
	var deferredIDs []string
	for i := range prepared {
		item := &prepared[i]
		deferUpdate := len(prepared) > 1 && item.completedPlan == nil && item.row.Plan.AgentEffect != nil && item.history == nil && item.row.Plan.HistoryEntryID == "" && string(item.response) == "{}"
		item.delivery, err = commitSourceMutationTxWithCache(ctx, tx, item.row, item.response, item.completedPlan, updatedAt, item.history, quotaBytes, item.recording, cache, !combineEvents, deferUpdate)
		if err != nil {
			for j := range prepared {
				prepared[j].row.freshOperation = false
			}
			return nil, err
		}
		if deferUpdate {
			deferredIDs = append(deferredIDs, item.row.ID)
		}
	}
	if err := cache.Flush(ctx, tx); err != nil {
		for j := range prepared {
			prepared[j].row.freshOperation = false
		}
		return nil, err
	}
	if len(deferredIDs) > 0 {
		updatedAtStr := updatedAt.Format(time.RFC3339Nano)
		const chunkSize = 256
		for start := 0; start < len(deferredIDs); start += chunkSize {
			end := min(start+chunkSize, len(deferredIDs))
			var sb strings.Builder
			sb.WriteString(`UPDATE source_mutations SET status=?, response_json='{}', error='', updated_at=?, plan_json='{}' WHERE id IN (`)
			args := make([]any, 0, (end-start)+2)
			args = append(args, sourceMutationCommitted, updatedAtStr)
			for i := start; i < end; i++ {
				if i > start {
					sb.WriteByte(',')
				}
				sb.WriteByte('?')
				args = append(args, deferredIDs[i])
			}
			sb.WriteByte(')')
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				for j := range prepared {
					prepared[j].row.freshOperation = false
				}
				return nil, err
			}
		}
	}
	if combineEvents && len(combinedChanges) > 0 {
		delivery, err := sourcefeed.EmitBatchTx(ctx, tx, combinedChanges)
		if err != nil {
			for j := range prepared {
				prepared[j].row.freshOperation = false
			}
			return nil, err
		}
		prepared[0].delivery = delivery
	}
	if err := tx.Commit(); err != nil {
		for j := range prepared {
			prepared[j].row.freshOperation = false
		}
		return nil, err
	}
	deliveries := make([]*sourcefeed.StagedDelivery, len(prepared))
	for i, item := range prepared {
		deliveries[i] = item.delivery
		item.row.freshOperation = false
		item.row.Status, item.row.Response, item.row.Error, item.row.UpdatedAt = sourceMutationCommitted, item.response, "", updatedAt
	}
	return deliveries, nil
}

func collectCombinedSourceChanges(prepared []preparedSettlement) ([]sourcefeed.Change, bool) {
	if len(prepared) <= 1 {
		return nil, false
	}
	changes := make([]sourcefeed.Change, 0, len(prepared))
	var projectID, workspaceID string
	var workspaceKind api.SourceWorkspaceKind
	for i := range prepared {
		row := prepared[i].row
		if !row.Plan.Changed {
			continue
		}
		for _, c := range row.Plan.sourceChanges() {
			pID := strings.TrimSpace(c.ProjectID)
			wID := strings.TrimSpace(c.WorkspaceID)
			if pID == "" || wID == "" {
				return nil, false
			}
			if len(changes) == 0 {
				projectID, workspaceID, workspaceKind = pID, wID, c.WorkspaceKind
			} else if pID != projectID || wID != workspaceID || c.WorkspaceKind != workspaceKind {
				return nil, false
			}
			changes = append(changes, c)
		}
	}
	return changes, true
}
