package invocation

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	var target [32]byte
	_, err := hex.Decode(target[:], []byte(s))
	return err == nil
}

// BeforeNativeOwner implements the existing admission consumer port. A committed
// start marker is a conservative recovery obligation, not proof of an effect.
// No operation is dispatched if the durable marker cannot be acknowledged.
func (r *SQLRecorder) BeforeNativeOwner(ctx context.Context, operation toolcontract.BoundOperation, modelCallID string) error {
	in := operation.Invocation()
	if err := in.Validate(); err != nil {
		return err
	}
	if in.Kind != toolcontract.ProgramChildInvocation || modelCallID == "" {
		return fmt.Errorf("native child requires actual outer model call")
	}
	if r == nil || r.database == nil {
		return fmt.Errorf("invocation recorder is not configured")
	}
	scope, err := json.Marshal(in.Scope)
	if err != nil {
		return err
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE invocation_program_executions SET state='running',owner_started=1,effect_certainty='unknown' WHERE id=? AND program_id=? AND node_id=? AND parent_execution_id=? AND actor_id=? AND scope_json=? AND definition_digest=? AND state IN ('prepared','waiting') AND EXISTS (SELECT 1 FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE p.id=invocation_program_executions.program_id AND p.state IN ('running','waiting') AND r.status='running' AND r.session_id=? AND r.tool_call_id=?)`, in.Identity.ExecutionID, in.Identity.ProgramID, in.Identity.NodeID, in.Identity.ParentExecutionID, in.ActorID, string(scope), operation.Definition().Digest(), in.SessionID, modelCallID)
	if err != nil {
		return err
	}
	if err = oneProgramTransition(result); err != nil {
		return err
	}
	return tx.Commit()
}

// WaitChild preserves one execution identity while approval or an owned job waits.
func (r *SQLRecorder) WaitChild(ctx context.Context, executionID, approvalReference string) error {
	if approvalReference == "" {
		return fmt.Errorf("waiting child requires authoritative wait reference")
	}
	result, err := r.database.ExecContext(ctx, `UPDATE invocation_program_executions SET state='waiting',approval_ref=? WHERE id=? AND state='prepared'`, approvalReference, executionID)
	if err != nil {
		return err
	}
	return oneProgramTransition(result)
}

// SettleChild preserves the exact typed owner result independently of a later
// authority stop. A failed ledger commit does not change the caller's result.
func (r *SQLRecorder) SettleChild(ctx context.Context, executionID string, in ChildSettlement) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if r == nil || r.database == nil {
		return fmt.Errorf("invocation recorder is not configured")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owner, state string
	var started bool
	if err = tx.QueryRowContext(ctx, `SELECT owner,state,owner_started FROM invocation_program_executions WHERE id=?`, executionID).Scan(&owner, &state, &started); err != nil {
		return err
	}
	result := in.Result
	if result.Evidence.Owner != "" && result.Evidence.Owner != owner {
		return fmt.Errorf("child settlement cannot replace subsystem owner")
	}
	if in.OwnerInvoked && !started {
		return fmt.Errorf("invoked child lacks durable start marker")
	}
	if !in.OwnerInvoked && result.EffectCertainty != toolcontract.NoEffect {
		return fmt.Errorf("uninvoked child cannot declare effects")
	}
	if started && !in.OwnerInvoked {
		return fmt.Errorf("started child requires explicit owner outcome or recovery reconciliation")
	}
	evidence := result.Evidence
	transition, err := tx.ExecContext(ctx, `UPDATE invocation_program_executions SET state=?,owner_invoked=?,status_code=?,stop_code=?,host_stop_code=?,output_publishable=?,complete=?,publication_eligible=?,publication_basis=?,effect_certainty=?,evidence_kind=?,evidence_ref=?,owner_ref=?,recovery_ref=?,held_version_id=?,accepted_revision=?,published_revision=?,detail_ref=?,settled_at=? WHERE id=? AND state IN ('prepared','waiting','running')`, string(result.Terminal), in.OwnerInvoked, result.Status, result.StopCode, in.HostStopCode, in.OutputPublishable, result.Complete, result.PublicationEligible, int(result.PublicationBasis), string(result.EffectCertainty), evidence.Kind, evidence.EvidenceRef, evidence.OwnerRef, evidence.RecoveryRef, evidence.HeldVersionID, in.AcceptedRevision, in.PublishedRevision, in.DetailReference, time.Now().UTC().Format(time.RFC3339Nano), executionID)
	if err != nil {
		return err
	}
	if err = oneProgramTransition(transition); err != nil {
		return err
	}
	return tx.Commit()
}

// ProgramSettlement is the process owner's aggregate statement. Child evidence
// remains independently queryable and is never rewritten by this transition.
type ProgramSettlement struct {
	State             string
	Status            *int
	StopCode          string
	DetailReference   string
	ProgressState     string
	ProgressReference string
}

func (s ProgramSettlement) validate() error {
	switch s.State {
	case "completed", "rejected", "failed", "interrupted":
	default:
		return fmt.Errorf("terminal program state required")
	}
	if s.Status != nil && (*s.Status < 0 || *s.Status > 255) {
		return fmt.Errorf("invalid aggregate utility status")
	}
	if s.DetailReference == "" {
		return fmt.Errorf("aggregate requires retained detail")
	}
	switch s.ProgressState {
	case "unchanged":
		if s.ProgressReference != "" {
			return fmt.Errorf("unchanged progress cannot claim update")
		}
	case "committed", "rejected", "unknown":
		if s.ProgressReference == "" {
			return fmt.Errorf("progress outcome requires owner reference")
		}
	default:
		return fmt.Errorf("explicit progress outcome required")
	}
	return nil
}

func (r *SQLRecorder) SettleProgram(ctx context.Context, programID string, in ProgramSettlement) error {
	if err := in.validate(); err != nil {
		return err
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Unfinished children must first be reconciled with their real owners. An
	// aggregate terminal cannot convert outstanding effects into a zero-effect stop.
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND state IN ('prepared','waiting','running')`, programID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("program has %d unsettled children", count)
	}
	transition, err := tx.ExecContext(ctx, `UPDATE invocation_programs SET state=?,status_code=?,stop_code=?,detail_ref=?,progress_state=?,progress_ref=?,settled_at=? WHERE id=? AND state IN ('running','waiting')`, in.State, in.Status, in.StopCode, in.DetailReference, in.ProgressState, in.ProgressReference, time.Now().UTC().Format(time.RFC3339Nano), programID)
	if err != nil {
		return err
	}
	if err = oneProgramTransition(transition); err != nil {
		return err
	}
	return tx.Commit()
}

func oneProgramTransition(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("program receipt transition refused")
	}
	return nil
}
