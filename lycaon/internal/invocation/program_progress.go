package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ReviewedProgressInputRetention captures the structured envelope owner's
// reviewed input. Progress has no fabricated source node or command spelling.
type ReviewedProgressInputRetention interface {
	RetainReviewedProgressInputs(context.Context, toolcontract.ProgramProgressIdentity, toolcontract.ReviewedOperationInputs) (string, error)
}

func (r ProgramAdmissionRecorder) BeforeProgressOwner(ctx context.Context, identity toolcontract.ProgramProgressIdentity, modelCallID string, inputs toolcontract.ReviewedOperationInputs) error {
	retainer, ok := r.Inputs.(ReviewedProgressInputRetention)
	if r.Recorder == nil || r.Recorder.database == nil || !ok || identity.ProgramID == "" || identity.ExecutionID == "" || identity.ActorID == "" || identity.SessionID == "" || identity.RootSessionID == "" || modelCallID == "" {
		return fmt.Errorf("progress start requires actual envelope, owner scope and reviewed retention")
	}
	if err := identity.Scope.Validate(); err != nil {
		return err
	}
	if err := validateReviewedInputs(inputs); err != nil {
		return err
	}
	reference, err := retainer.RetainReviewedProgressInputs(ctx, identity, inputs)
	if err != nil {
		return err
	}
	if reference == "" {
		return fmt.Errorf("progress retention returned no reference")
	}
	scope, err := json.Marshal(identity.Scope)
	if err != nil {
		return err
	}
	tx, err := r.Recorder.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Current root scope is read from P09's progress owner immediately before
	// this marker. The model parent and addressed program must still be running.
	result, err := tx.ExecContext(ctx, `INSERT INTO invocation_program_progress (execution_id,program_id,actor_id,session_id,root_session_id,scope_json,arguments_representation,arguments_digest,arguments_ref,state,started_at) SELECT ?,p.id,?,?,?,?,?,?,?,'running',? FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE p.id=? AND p.syntax_validated=1 AND p.state='running' AND p.progress_state='unchanged' AND p.progress_ref='' AND r.status='running' AND r.session_id=? AND r.tool_call_id=?`, identity.ExecutionID, identity.ActorID, identity.SessionID, identity.RootSessionID, string(scope), inputs.Representation(), inputs.ArgumentsDigest(), reference, time.Now().UTC().Format(time.RFC3339Nano), identity.ProgramID, identity.SessionID, modelCallID)
	if err != nil {
		return err
	}
	if err = oneProgramTransition(result); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE invocation_programs SET progress_state='unknown',progress_ref=? WHERE id=?`, identity.ExecutionID, identity.ProgramID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ProgressSettlement is the actual AuthorUpdate owner's outcome. Updated content
// is never proof of shell execution or task completion. An ambiguous owner error
// is recorded unknown; this API provides no automatic progress replay.
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
	result, err := tx.ExecContext(ctx, `UPDATE invocation_program_progress SET state=?,owner_invoked=?,detail_ref=?,settled_at=? WHERE execution_id=? AND program_id=? AND actor_id=? AND session_id=? AND root_session_id=? AND scope_json=? AND state='running'`, in.State, in.OwnerInvoked, in.DetailReference, time.Now().UTC().Format(time.RFC3339Nano), id.ExecutionID, id.ProgramID, id.ActorID, id.SessionID, id.RootSessionID, string(scope))
	if err != nil {
		return err
	}
	if err = oneProgramTransition(result); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE invocation_programs SET progress_state=?,progress_ref=? WHERE id=?`, in.State, id.ExecutionID, id.ProgramID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecordProgressRefusal retains an existing owner's structural preflight refusal
// without claiming invocation or overwriting an already accepted update.
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
