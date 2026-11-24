package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// CheckOperationProgress checks reached operation prerequisites against current missing/workflow facts.
func CheckOperationProgress(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, missing, workerChild, reviewLoopActive bool) error {
	if definition.Digest() == "" {
		return fmt.Errorf("progress admission requires frozen operation")
	}
	facts := definition.Facts()
	required, err := operationProgressRequired(facts)
	if err != nil {
		return err
	}
	if !required || !progress.PrerequisiteMissing(missing, workerChild, reviewLoopActive) {
		return nil
	}
	return &toolrejection.ToolReject{
		Code:     "PROGRESS_MISSING",
		OwnerRef: facts.Contract.Owner,
		Data: map[string]any{
			"tool":              facts.Identity,
			"source_start_byte": invocation.Span.StartByte,
			"source_end_byte":   invocation.Span.EndByte,
			"source_line":       invocation.Span.Line,
			"source_column":     invocation.Span.Column,
			"source_unit_id":    invocation.SourceUnitID,
		},
	}
}

// CheckOperationProgressState validates session ownership, reconciliation, and missing-plan gates.
func CheckOperationProgressState(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, state progress.PrerequisiteState) error {
	if state.SessionID != invocation.SessionID || state.RootSessionID == "" {
		return fmt.Errorf("operation progress state does not match invocation")
	}
	if err := checkProgressReconciliation(definition, invocation, state); err != nil {
		return err
	}
	return CheckOperationProgress(definition, invocation, state.Missing, state.WorkerChild, state.ReviewLoopActive)
}

// CheckReachedMutationProgress checks progress prerequisites at the attached-root write door.
func CheckReachedMutationProgress(ctx context.Context, provider ProgramProgressFacts, definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, rootID, path string) error {
	if rootID == "" {
		return nil
	}
	if provider == nil {
		return fmt.Errorf("native mutation requires current progress facts")
	}
	state, err := provider.ProgramProgressState(ctx, invocation.SessionID)
	if err != nil {
		return err
	}
	if state.SessionID != invocation.SessionID || state.RootSessionID == "" {
		return fmt.Errorf("mutation progress state does not match invocation")
	}
	if err := checkProgressReconciliation(definition, invocation, state); err != nil {
		return err
	}
	if !progress.PrerequisiteMissing(state.Missing, state.WorkerChild, state.ReviewLoopActive) {
		return nil
	}
	facts := definition.Facts()
	return &toolrejection.ToolReject{
		Code:     "PROGRESS_MISSING",
		OwnerRef: facts.Contract.Owner,
		Data: map[string]any{
			"tool":              facts.Identity,
			"path":              path,
			"source_start_byte": invocation.Span.StartByte,
			"source_end_byte":   invocation.Span.EndByte,
			"source_line":       invocation.Span.Line,
			"source_column":     invocation.Span.Column,
			"source_unit_id":    invocation.SourceUnitID,
		},
	}
}

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
	handles := make([]string, 0, len(state.Reconciliation.Unaccounted))
	for _, u := range state.Reconciliation.Unaccounted {
		if u.Handle != "" {
			handles = append(handles, u.Handle)
		}
	}
	return &toolrejection.ToolReject{
		Code: "PROGRESS_RECONCILIATION_REQUIRED",
		Data: map[string]any{
			"tool":                facts.Identity,
			"pending":             state.OpenSteps,
			"root_session_id":     state.RootSessionID,
			"settled_work":        state.Reconciliation.Settled,
			"unaccounted_handles": handles,
			"source_start_byte":   invocation.Span.StartByte,
			"source_end_byte":     invocation.Span.EndByte,
			"source_line":         invocation.Span.Line,
			"source_column":       invocation.Span.Column,
			"source_unit_id":      invocation.SourceUnitID,
		},
	}
}
