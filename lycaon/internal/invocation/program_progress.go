package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (r ProgramAdmissionRecorder) BeforeProgressOwner(ctx context.Context, identity toolcontract.ProgramProgressIdentity, modelCallID string, inputs toolcontract.ReviewedOperationInputs) error {
	if r.Recorder == nil || r.Recorder.database == nil || r.Inputs.Outputs == nil || identity.ProgramID == "" || identity.ExecutionID == "" || identity.ActorID == "" || identity.SessionID == "" || identity.RootSessionID == "" || modelCallID == "" {
		return fmt.Errorf("progress start requires actual envelope, owner scope and reviewed retention")
	}
	if err := identity.Scope.Validate(); err != nil {
		return err
	}
	if err := validateReviewedInputs(inputs); err != nil {
		return err
	}
	if identity.ProgramID != r.Inputs.ProgramID || identity.SessionID != r.Inputs.Scope.SessionID || identity.RootSessionID != r.Inputs.Scope.RootSessionID || identity.Scope.ProjectID != r.Inputs.Scope.ProjectID {
		return fmt.Errorf("reviewed input capture does not match actual progress scope")
	}
	scope, err := json.Marshal(identity.Scope)
	if err != nil {
		return err
	}
	prepared := preparedProgressStart{identity: identity, modelCallID: modelCallID, inputs: inputs, scope: scope}
	start := reviewedOwnerStart{programID: identity.ProgramID, executionID: identity.ExecutionID, progress: true, record: prepared.record}
	return r.Inputs.startReviewedOwner(ctx, r.Recorder, start, inputs)
}

type preparedProgressStart struct {
	identity    toolcontract.ProgramProgressIdentity
	inputs      toolcontract.ReviewedOperationInputs
	modelCallID string
	scope       []byte
}

func (s preparedProgressStart) record(ctx context.Context, tx *sql.Tx, reference string) error {
	identity, inputs, scope := s.identity, s.inputs, s.scope
	if err := chargeProgramTrace(ctx, tx, identity.ProgramID, 1, int64(len(identity.ExecutionID)+len(identity.ActorID)+len(identity.SessionID)+len(identity.RootSessionID)+len(scope)+len(inputs.Representation())+len(inputs.ArgumentsDigest())+len(reference)), traceChargeProgressStart); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO invocation_program_progress (execution_id,program_id,actor_id,session_id,root_session_id,scope_json,arguments_representation,arguments_digest,arguments_ref,state,started_at) SELECT ?,p.id,?,?,?,?,?,?,?,'running',? FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE p.id=? AND p.syntax_validated=1 AND p.state='running' AND p.progress_state='unchanged' AND p.progress_ref='' AND r.status='running' AND r.session_id=? AND r.tool_call_id=?`, identity.ExecutionID, identity.ActorID, identity.SessionID, identity.RootSessionID, string(scope), inputs.Representation(), inputs.ArgumentsDigest(), reference, time.Now().UTC().Format(time.RFC3339Nano), identity.ProgramID, identity.SessionID, s.modelCallID)
	if err != nil {
		return err
	}
	if err := oneProgramTransition(result); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE invocation_programs SET progress_state='unknown',progress_ref=? WHERE id=?`, identity.ExecutionID, identity.ProgramID)
	if err != nil {
		return err
	}
	return nil
}

// ProgressSettlement records the owner outcome without automatic replay.
type ProgressSettlement struct {
	Identity        toolcontract.ProgramProgressIdentity
	OwnerInvoked    bool
	State           string
	DetailReference string
}

func (r *SQLRecorder) SettleProgramProgress(ctx context.Context, in ProgressSettlement) error {
	if r == nil || r.database == nil || in.DetailReference == "" {
		return fmt.Errorf("progress settlement requires recorder and owner detail")
	}
	switch in.State {
	case "unchanged", "committed":
		if !in.OwnerInvoked {
			return fmt.Errorf("acknowledged progress requires observed owner invocation")
		}
	case "rejected", "unknown":
	default:
		return fmt.Errorf("invalid progress outcome")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id := in.Identity
	scope, err := json.Marshal(id.Scope)
	if err != nil {
		return err
	}
	if err = chargeProgramTrace(ctx, tx, id.ProgramID, 1, int64(len(in.State)+len(in.DetailReference)+32), traceChargeProgressSettlement); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE invocation_program_progress SET state=?,owner_invoked=?,detail_ref=?,settled_at=? WHERE execution_id=? AND program_id=? AND actor_id=? AND session_id=? AND root_session_id=? AND scope_json=? AND state='running'`, in.State, in.OwnerInvoked, in.DetailReference, time.Now().UTC().Format(time.RFC3339Nano), id.ExecutionID, id.ProgramID, id.ActorID, id.SessionID, id.RootSessionID, string(scope))
	if err != nil {
		return err
	}
	if err := oneProgramTransition(result); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE invocation_programs SET progress_state=?,progress_ref=? WHERE id=?`, in.State, id.ExecutionID, id.ProgramID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecordProgressRefusal preserves an accepted update and records no invocation.
func (r *SQLRecorder) RecordProgressRefusal(ctx context.Context, programID, ownerReference string) error {
	if r == nil || r.database == nil || ownerReference == "" {
		return fmt.Errorf("progress refusal requires authoritative owner reference")
	}
	result, err := r.database.ExecContext(ctx, `UPDATE invocation_programs SET progress_state='rejected',progress_ref=? WHERE id=? AND progress_state='unchanged' AND progress_ref='' AND NOT EXISTS (SELECT 1 FROM invocation_program_progress p WHERE p.program_id=invocation_programs.id)`, ownerReference, programID)
	if err != nil {
		return err
	}
	return oneProgramTransition(result)
}
