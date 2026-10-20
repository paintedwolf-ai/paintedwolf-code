package invocation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
)

// ProgramFeedbackAdmission charges retained feedback before SQL strings or JSON
// containers are materialized. The reservation survives the read transaction.
type ProgramFeedbackAdmission interface {
	ReserveProgramState(context.Context, int64) (func(), error)
}

type ProgramFeedbackLease struct {
	Binding            ProgramExecutionBinding
	Projection         ProgramProjection
	RequiredStateBytes int64
	release            func()
	once               sync.Once
}

func (l *ProgramFeedbackLease) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() { l.Binding = ProgramExecutionBinding{}; l.Projection = ProgramProjection{}; l.release() })
}

// ReadProgramFeedback measures and hydrates the exact same read snapshot. A
// concurrent settlement cannot enlarge the selected rows after admission. No
// retained body, definition JSON or reviewed-argument body is loaded here.
func (r *SQLRecorder) ReadProgramFeedback(ctx context.Context, session, program, execution string, maximum int, admission ProgramFeedbackAdmission) (*ProgramFeedbackLease, error) {
	if r == nil || r.database == nil || admission == nil || ctx == nil || maximum < 1 || maximum > 16 {
		return nil, fmt.Errorf("feedback requires bounded snapshot and state admission")
	}
	baseRelease, err := admission.ReserveProgramState(ctx, 64<<10)
	if err != nil {
		return nil, err
	}
	if baseRelease == nil {
		return nil, fmt.Errorf("feedback base admission returned no release")
	}
	baseOwned := true
	defer func() {
		if baseOwned {
			baseRelease()
		}
	}()
	var tx *sql.Tx
	if reader, ok := r.database.(db.ReadHandle); ok {
		tx, err = reader.BeginReadTx(ctx)
	} else {
		tx, err = r.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	required, err := feedbackStateBytes(ctx, tx, session, program, execution, maximum)
	if err != nil {
		return nil, err
	}
	release, err := admission.ReserveProgramState(ctx, required-(64<<10))
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("feedback admission returned no release")
	}
	out := &ProgramFeedbackLease{release: func() { release(); baseRelease() }, RequiredStateBytes: required}
	baseOwned = false
	accepted := false
	defer func() {
		if !accepted {
			out.Close()
		}
	}()
	out.Binding, err = readProgramExecutionBinding(ctx, tx, session, execution)
	if err != nil {
		return nil, err
	}
	if out.Binding.Invocation.Identity.ProgramID != program {
		return nil, fmt.Errorf("feedback binding belongs to another program")
	}
	out.Projection, err = projectProgram(ctx, tx, session, program, maximum)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	accepted = true
	return out, nil
}

// SQL length on BLOB measures bytes without returning field contents to Go.
// The factor32 covers driver copies, decoded JSON string slots and both selected
// row and final projection containers; fixed slack covers scalar structs/rows.
func feedbackStateBytes(ctx context.Context, tx *sql.Tx, session, program, execution string, maximum int) (int64, error) {
	var total int64
	add := func(query string, args ...any) error {
		var n int64
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			return err
		}
		if n < 0 || n > 1<<30 || total > 1<<30-n {
			return fmt.Errorf("feedback metadata exceeds supported byte ceiling")
		}
		total += n
		return nil
	}
	if err := add(`SELECT `+sqlByteSum("p.id,p.parent_receipt_id,p.catalog_digest,p.source_unit_id,p.source_digest,p.source_ref,p.state,p.stop_code,p.detail_ref,p.progress_state,p.progress_ref,p.started_at,p.settled_at,r.tool_call_id,r.tool_name,r.session_id")+` FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE p.id=? AND r.session_id=?`, program, session); err != nil {
		return 0, err
	}
	if err := add(`SELECT COALESCE(sum(`+sqlByteSum("execution_id,program_id,root_session_id,state,arguments_digest,arguments_ref,detail_ref")+`),0) FROM invocation_program_progress WHERE program_id=?`, program); err != nil {
		return 0, err
	}
	if err := add(`SELECT `+sqlByteSum("e.id,e.program_id,e.node_id,e.parent_execution_id,e.actor_id,e.scope_json,e.definition_digest,n.source_unit_id,s.source_id,s.revision,s.representation,s.sha256")+` FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id JOIN invocation_receipts r ON r.id=p.parent_receipt_id JOIN invocation_program_nodes n ON n.program_id=e.program_id AND n.node_id=e.node_id JOIN invocation_program_sources s ON s.program_id=n.program_id AND s.source_unit_id=n.source_unit_id WHERE e.id=? AND e.program_id=? AND r.session_id=?`, execution, program, session); err != nil {
		return 0, err
	}
	columns := "e.id,e.program_id,e.node_id,e.parent_execution_id,e.owner,e.lifecycle,e.definition_digest,e.arguments_digest,e.reviewed_arguments_digest,e.reviewed_arguments_ref,e.native_command,e.effect_mode,e.effect_classes_json,e.arguments_ref,e.canonical,e.state,e.stop_code,e.host_stop_code,e.effect_certainty,e.evidence_kind,e.evidence_ref,e.owner_ref,e.recovery_ref,e.held_version_id,e.accepted_revision,e.published_revision,e.detail_ref,e.approval_ref,n.source_unit_id"
	for _, kind := range []string{"completed", "uncertain", "stopped"} {
		limit := maximum
		if kind == "stopped" {
			limit = 1
		}
		condition, err := executionProjectionCondition(kind)
		if err != nil {
			return 0, err
		}
		query := `SELECT COALESCE(sum(bytes),0) FROM (SELECT ` + sqlByteSum(columns) + ` AS bytes FROM invocation_program_executions e JOIN invocation_program_nodes n ON n.program_id=e.program_id AND n.node_id=e.node_id WHERE e.program_id=? AND ` + condition + ` ORDER BY e.ordinal DESC LIMIT ?)`
		if err = add(query, program, limit); err != nil {
			return 0, err
		}
	}
	return 64<<10 + total*32 + int64(2*maximum+1)*4096, nil
}

func sqlByteSum(columns string) string {
	parts := strings.Split(columns, ",")
	for i, column := range parts {
		parts[i] = "COALESCE(length(CAST(" + column + " AS BLOB)),0)"
	}
	return strings.Join(parts, "+")
}
