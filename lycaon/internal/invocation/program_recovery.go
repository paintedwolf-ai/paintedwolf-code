package invocation

import (
	"context"
	"fmt"
	"time"
)

// InterruptPrograms records host loss without retrying an interpreter or calling
// an external producer. Completed child rows are untouched. An unfinished reached
// admission may already have realized authority, even before the owner marker.
func (r *SQLRecorder) InterruptPrograms(ctx context.Context, sessionID, detailReference string) (int64, error) {
	if r == nil || r.database == nil {
		return 0, fmt.Errorf("invocation recorder is not configured")
	}
	if sessionID == "" || detailReference == "" {
		return 0, fmt.Errorf("interruption requires session and retained recovery detail")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `UPDATE invocation_program_executions SET state='interrupted',status_code=NULL,host_stop_code='TOOL_OWNER_INTERRUPTED',output_publishable=0,complete=0,publication_eligible=0,effect_certainty='unknown',detail_ref=?,settled_at=? WHERE state IN ('prepared','waiting','running') AND program_id IN (SELECT p.id FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=?)`, detailReference, at, sessionID)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE invocation_program_progress SET state='unknown',detail_ref=?,settled_at=? WHERE state='running' AND session_id=?`, detailReference, at, sessionID)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE invocation_programs SET state='interrupted',status_code=NULL,stop_code='TOOL_OWNER_INTERRUPTED',detail_ref=?,settled_at=? WHERE state IN ('running','waiting') AND parent_receipt_id IN (SELECT id FROM invocation_receipts WHERE session_id=?)`, detailReference, at, sessionID)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// MarkUntaken records only known syntax proven untaken by the evaluator. It
// cannot erase a reached node, or enumerate future loop executions.
func (r *SQLRecorder) MarkUntaken(ctx context.Context, programID, nodeID string) error {
	if r == nil || r.database == nil {
		return fmt.Errorf("invocation recorder is not configured")
	}
	result, err := r.database.ExecContext(ctx, `UPDATE invocation_program_nodes SET state='untaken' WHERE program_id=? AND node_id=? AND state='not_started' AND EXISTS (SELECT 1 FROM invocation_programs p WHERE p.id=program_id AND p.state IN ('running','waiting'))`, programID, nodeID)
	if err != nil {
		return err
	}
	return oneProgramTransition(result)
}
