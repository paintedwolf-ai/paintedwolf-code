package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// CheckOperationProgress is called for known admission prerequisites and again
// before reached effects using current owner content and workflow facts. It does
// not inspect or execute untaken branches and never changes checklist content.
func CheckOperationProgress(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, content string, workerChild, reviewLoopActive bool) error {
	if definition.Digest() == "" {
		return fmt.Errorf("progress admission requires frozen operation")
	}
	facts := definition.Facts()
	required, err := operationProgressRequired(facts)
	if err != nil {
		return err
	}
	if !required || !progress.PrerequisiteMissing(content, workerChild, reviewLoopActive) {
		return nil
	}
	return &toolrejection.ToolReject{Code: "PROGRESS_MISSING", OwnerRef: facts.Contract.Owner, Data: map[string]any{"tool": facts.Identity, "source_start_byte": invocation.Span.StartByte, "source_end_byte": invocation.Span.EndByte, "source_line": invocation.Span.Line, "source_column": invocation.Span.Column, "source_unit_id": invocation.SourceUnitID}}
}

// CheckOperationProgressState prevents cross-session progress from satisfying a
// reached native prerequisite. The provider reads the existing session tree.
func CheckOperationProgressState(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, state progress.PrerequisiteState) error {
	if state.SessionID != invocation.SessionID || state.RootSessionID == "" {
		return fmt.Errorf("operation progress state does not match invocation")
	}
	if err := checkProgressReconciliation(definition, invocation, state); err != nil {
		return err
	}
	return CheckOperationProgress(definition, invocation, state.Content, state.WorkerChild, state.ReviewLoopActive)
}

// checkReachedMutationProgress checks the current checklist at the write door.
// The caller supplies a resolved attached root, so scratch and output writes
// never acquire a project-file prerequisite.
func checkReachedMutationProgress(ctx context.Context, provider ProgramProgressFacts, definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, rootID, path string) error {
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
	if !progress.PrerequisiteMissing(state.Content, state.WorkerChild, state.ReviewLoopActive) {
		return nil
	}
	facts := definition.Facts()
	return &toolrejection.ToolReject{Code: "PROGRESS_MISSING", OwnerRef: facts.Contract.Owner, Data: map[string]any{
		"tool": facts.Identity, "path": path, "source_start_byte": invocation.Span.StartByte,
		"source_end_byte": invocation.Span.EndByte, "source_line": invocation.Span.Line,
		"source_column": invocation.Span.Column, "source_unit_id": invocation.SourceUnitID,
	}}
}
