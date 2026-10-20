package toolexecution

import (
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
	required := false
	switch facts.ProgressGate {
	case toolcontract.ProgressRequired:
		required = true
	case toolcontract.ProgressNotRequired:
	case toolcontract.ProgressExistingAdmission:
		required = progress.IsProgressGatedTool(facts.Identity)
	default:
		return fmt.Errorf("unknown operation progress policy")
	}
	if !required || !progress.PrerequisiteMissing(content, workerChild, reviewLoopActive) {
		return nil
	}
	return &toolrejection.ToolReject{Code: "PROGRESS_MISSING", OwnerRef: facts.Contract.Owner, Data: map[string]any{"tool": facts.Identity, "source_start_byte": invocation.Span.StartByte, "source_end_byte": invocation.Span.EndByte, "source_line": invocation.Span.Line, "source_column": invocation.Span.Column, "source_unit_id": invocation.SourceUnitID}}
}
