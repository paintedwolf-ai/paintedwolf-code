package persistence

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func (s *Verdicts) ReadVerdictReceipts(ctx context.Context, runID string) ([]runstate.VerdictReceipt, error) {
	operations, err := s.transactions.queries.ListWorkflowVerdictReceipts(ctx, runID)
	if err != nil {
		return nil, err
	}
	receipts := make([]runstate.VerdictReceipt, 0, len(operations))
	for _, operation := range operations {
		receipt := runstate.VerdictReceipt{
			SourceRevision: operation.SourceRevision, ToolCallID: operation.ToolCallID,
			RunID: operation.RunID, Phase: operation.Phase, Status: operation.Status,
		}
		if err := json.Unmarshal([]byte(operation.EvidenceJson), &receipt.Submission); err != nil {
			return nil, err
		}
		if operation.ResponseJson != "" {
			if err := json.Unmarshal([]byte(operation.ResponseJson), &receipt.Outcome); err != nil {
				return nil, err
			}
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}
