package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// preparedNativeStart holds the reviewed facts encoded before writer admission.
type preparedNativeStart struct {
	operation      toolcontract.BoundOperation
	inputs         toolcontract.ReviewedOperationInputs
	modelCallID    string
	scope, classes []byte
}

func prepareNativeStart(operation toolcontract.BoundOperation, modelCallID string, inputs toolcontract.ReviewedOperationInputs) (preparedNativeStart, error) {
	start := preparedNativeStart{operation: operation, inputs: inputs, modelCallID: modelCallID}
	in := operation.Invocation()
	if err := in.Validate(); err != nil {
		return start, err
	}
	if in.Kind != toolcontract.ProgramChildInvocation || modelCallID == "" || !validDigest(inputs.ArgumentsDigest()) {
		return start, fmt.Errorf("native child requires actual outer model call")
	}
	var err error
	start.classes, err = json.Marshal(operation.Effects().Classes())
	if err != nil {
		return start, err
	}
	start.scope, err = json.Marshal(in.Scope)
	return start, err
}

// A committed start marker requires recovery if no owner result is recorded.
func (s preparedNativeStart) record(ctx context.Context, tx *sql.Tx, reference string) error {
	operation := s.operation
	in := operation.Invocation()
	digest, representation := s.inputs.ArgumentsDigest(), s.inputs.Representation()
	if err := chargeProgramTrace(ctx, tx, in.Identity.ProgramID, 1, int64(len(digest)+len(reference)+len(representation)+len(operation.Command())+len(operation.Mode())+len(s.classes)), traceChargeNonTerminal); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE invocation_program_executions SET state='running',owner_started=1,effect_certainty='unknown',reviewed_arguments_digest=?,reviewed_arguments_ref=?,reviewed_arguments_representation=?,native_command=?,effect_mode=?,effect_classes_json=? WHERE id=? AND program_id=? AND node_id=? AND parent_execution_id=? AND actor_id=? AND scope_json=? AND definition_digest=? AND state IN ('prepared','waiting') AND EXISTS (SELECT 1 FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE p.id=invocation_program_executions.program_id AND p.state IN ('running','waiting') AND r.status='running' AND r.session_id=? AND r.tool_call_id=?)`, digest, reference, representation, operation.Command(), operation.Mode(), string(s.classes), in.Identity.ExecutionID, in.Identity.ProgramID, in.Identity.NodeID, in.Identity.ParentExecutionID, in.ActorID, string(s.scope), operation.Definition().Digest(), in.SessionID, s.modelCallID)
	if err != nil {
		return err
	}
	return oneProgramTransition(result)
}
