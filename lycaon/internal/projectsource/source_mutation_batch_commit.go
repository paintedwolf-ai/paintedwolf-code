package projectsource

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type preparedSettlement struct {
	row       *sourceMutationRow
	history   *sourceHistoryEntry
	recording sourceledger.PreparedRecording
	response  json.RawMessage
	delivery  *sourcefeed.StagedDelivery
}

// commitRows settles individually journaled effects in one attribution
// transaction. Content preparation precedes the writer transaction.
func (s *sourceSettlement) commitRows(ctx context.Context, rows []*sourceMutationRow) error {
	deliveries, err := s.commitRowsOnce(ctx, rows)
	if err != nil {
		return err
	}
	for i, row := range rows {
		deliveries[i].DeliverCommitted()
		notifySourceMutation(ctx, row.Plan)
	}
	return nil
}

func (s *sourceSettlement) commitRowsOnce(ctx context.Context, rows []*sourceMutationRow) ([]*sourcefeed.StagedDelivery, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	prepared := make([]preparedSettlement, 0, len(rows))
	defer func() {
		for _, item := range prepared {
			if item.recording != nil {
				item.recording.Close()
			}
		}
	}()
	for _, row := range rows {
		history, err := s.History.buildSourceHistoryEntry(ctx, row.ID, &row.Plan)
		if err != nil {
			return nil, err
		}
		item := preparedSettlement{row: row, history: history, response: append(json.RawMessage(nil), row.Plan.Response...)}
		if row.Plan.Changed && s.recorder != nil {
			item.recording, err = s.recorder.Prepare(ctx, row.Plan.ledgerInputs(row.ID))
			if err != nil {
				return nil, err
			}
		}
		prepared = append(prepared, item)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	updatedAt := time.Now().UTC()
	for i := range prepared {
		item := &prepared[i]
		item.delivery, err = commitSourceMutationTx(ctx, tx, item.row, item.response, updatedAt, item.history, s.History.recoveryQuotaBytes(), item.recording)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	deliveries := make([]*sourcefeed.StagedDelivery, len(prepared))
	for i, item := range prepared {
		deliveries[i] = item.delivery
		item.row.Status, item.row.Response, item.row.Error, item.row.UpdatedAt = sourceMutationCommitted, item.response, "", updatedAt
	}
	return deliveries, nil
}
