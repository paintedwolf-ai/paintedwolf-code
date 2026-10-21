package invocation

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// maxExecutionRevisionPins bounds what one dynamic execution may pin.
const maxExecutionRevisionPins = 1024

// RecordRevisionPins retains the revisions one execution captured before its
// owner prepared an effect. Pins append in capture order while the execution
// is unsettled; resubmitting the same pins is idempotent and different pins at
// a recorded position are refused rather than refreshed.
func (r *SQLRecorder) RecordRevisionPins(ctx context.Context, executionID string, pins []toolcontract.ExecutionRevision) error {
	if r == nil || r.database == nil {
		return fmt.Errorf("invocation recorder is not configured")
	}
	if executionID == "" || len(pins) == 0 {
		return fmt.Errorf("revision pins require an execution and at least one capture")
	}
	for _, pin := range pins {
		if err := pin.Validate(); err != nil {
			return err
		}
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var program, state string
	if err = tx.QueryRowContext(ctx, `SELECT program_id,state FROM invocation_program_executions WHERE id=?`, executionID).Scan(&program, &state); err != nil {
		return err
	}
	if state != "prepared" && state != "waiting" && state != "running" {
		return fmt.Errorf("settled execution cannot gain revision pins")
	}
	existing, err := readRevisionPins(ctx, tx, executionID)
	if err != nil {
		return err
	}
	for i, pin := range pins {
		if i < len(existing) {
			if existing[i] != pin {
				return fmt.Errorf("revision pin %d conflicts with the recorded capture", i)
			}
			continue
		}
		if i >= maxExecutionRevisionPins {
			return fmt.Errorf("execution exceeds %d revision pins", maxExecutionRevisionPins)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_revision_pins(program_id,execution_id,ordinal,role,mode,basis,root_id,path,document_id,document_revision,disk_sha256,representation,content_sha256,content_bytes,captured_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			program, executionID, i, string(pin.Role), string(pin.Mode), string(pin.Basis), pin.RootID, pin.Path, pin.DocumentID, pin.DocumentRevision, pin.DiskSHA256, string(pin.Representation), pin.ContentSHA256, pin.ContentBytes, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RevisionPins returns one session's execution captures in recorded order.
func (r *SQLRecorder) RevisionPins(ctx context.Context, sessionID, executionID string) ([]toolcontract.ExecutionRevision, error) {
	if r == nil || r.database == nil {
		return nil, fmt.Errorf("invocation recorder is not configured")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE e.id=? AND r.session_id=?`, executionID, sessionID).Scan(&count); err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, fmt.Errorf("execution is not visible to this session")
	}
	return readRevisionPins(ctx, tx, executionID)
}

func readRevisionPins(ctx context.Context, tx *sql.Tx, executionID string) ([]toolcontract.ExecutionRevision, error) {
	rows, err := tx.QueryContext(ctx, `SELECT role,mode,basis,root_id,path,document_id,document_revision,disk_sha256,representation,content_sha256,content_bytes FROM invocation_program_revision_pins WHERE execution_id=? ORDER BY ordinal LIMIT ?`, executionID, maxExecutionRevisionPins)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []toolcontract.ExecutionRevision
	for rows.Next() {
		var pin toolcontract.ExecutionRevision
		if err := rows.Scan(&pin.Role, &pin.Mode, &pin.Basis, &pin.RootID, &pin.Path, &pin.DocumentID, &pin.DocumentRevision, &pin.DiskSHA256, &pin.Representation, &pin.ContentSHA256, &pin.ContentBytes); err != nil {
			return nil, err
		}
		out = append(out, pin)
	}
	return out, rows.Err()
}
