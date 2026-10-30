package toolexecution

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func operationProgressRequired(facts toolcontract.OperationFacts) (bool, error) {
	switch facts.ProgressGate {
	case toolcontract.ProgressRequired:
		return true, nil
	case toolcontract.ProgressNotRequired:
		return false, nil
	case toolcontract.ProgressExistingAdmission:
		return progress.IsProgressGatedTool(facts.Identity), nil
	default:
		return false, fmt.Errorf("unknown operation progress policy")
	}
}

func checkProgressReconciliation(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, state progress.PrerequisiteState) error {
	if state.WorkerChild || state.Reconciliation == nil {
		return nil
	}
	facts := definition.Facts()
	required, err := operationProgressRequired(facts)
	if err != nil {
		return err
	}
	if !required {
		return nil
	}
	return &toolrejection.ToolReject{Code: "PROGRESS_ITEM_NOT_CLOSED", OwnerRef: "progress", Data: map[string]any{
		"tool": facts.Identity, "pending": progress.OpenStepCount(state.Content),
		"settled_work": state.Reconciliation.Jobs, "reconciliation_token": state.Reconciliation.Token,
		"source_start_byte": invocation.Span.StartByte, "source_end_byte": invocation.Span.EndByte,
		"source_line": invocation.Span.Line, "source_column": invocation.Span.Column, "source_unit_id": invocation.SourceUnitID,
	}}
}
