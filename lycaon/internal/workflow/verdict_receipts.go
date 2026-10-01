package workflow

import (
	"context"
	"encoding/json"
)

// VerdictReceipt binds a structured submission and its audit to one phase.
type VerdictReceipt struct {
	SourceRevision int64                    `json:"source_revision"`
	ToolCallID     string                   `json:"tool_call_id"`
	RunID          string                   `json:"run_id"`
	Phase          string                   `json:"phase"`
	Status         string                   `json:"status"`
	Submission     VerdictSubmission        `json:"submission"`
	Outcome        ReviewLoopVerdictOutcome `json:"outcome"`
}

func (s *SQLStore) ReadVerdictReceipts(ctx context.Context, runID string) ([]VerdictReceipt, error) {
	operations, err := s.queries.ListWorkflowVerdictReceipts(ctx, runID)
	if err != nil {
		return nil, err
	}
	receipts := make([]VerdictReceipt, 0, len(operations))
	for _, operation := range operations {
		receipt := VerdictReceipt{
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
