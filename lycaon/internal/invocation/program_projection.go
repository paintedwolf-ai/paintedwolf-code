package invocation

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProgramProjection contains bounded ledger facts for operation-aware feedback.
// OAR owns dispositions and copy; workflow owners supply outstanding obligations.
// Static counts never predict future loop or function invocations.
type ProgramProjection struct {
	Program                   api.CommandProgramReceipt
	Effects                   []ExecutionProjection
	Unknown                   []ExecutionProjection
	Stopped                   *ExecutionProjection
	CompletedEffects          int
	UncertainEffects          int
	ExecutedChildren          int
	ExecutedReads             int
	StaticNotStarted          int
	StaticUntaken             int
	OmittedEffects            int
	OmittedUnknown            int
	DetailReference           string
	FreshInvocationReference  string
	FreshInvocationLimitation string
}

// ExecutionProjection keeps the real dynamic identity beside its static span.
type ExecutionProjection struct {
	Receipt      api.CommandExecutionReceipt
	SourceUnitID string
	Span         toolcontract.SourceSpan
}

// ProjectProgram does not infer effects from a command, stdout or utility status.
// A completed allowed read excludes whole-program resubmission even with NoEffect.
func (r *SQLRecorder) ProjectProgram(ctx context.Context, sessionID, programID string, maximum int) (ProgramProjection, error) {
	var out ProgramProjection
	if r == nil || r.database == nil {
		return out, fmt.Errorf("invocation recorder is not configured")
	}
	if maximum < 1 || maximum > 16 {
		return out, fmt.Errorf("program projection limit must be 1..16")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	out.Program, err = readProgram(ctx, tx, sessionID, programID)
	if err != nil {
		return out, err
	}
	out.DetailReference = out.Program.DetailRef
	var reached, unsettled int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE effect_certainty='completed'),count(*) FILTER (WHERE effect_certainty IN ('unknown','attempted')),count(*) FILTER (WHERE owner_invoked=1),count(*) FILTER (WHERE owner_invoked=1 AND lifecycle='read_only'),count(*) FILTER (WHERE owner_started=1 OR owner_invoked=1),count(*) FILTER (WHERE state IN ('prepared','waiting','running')) FROM invocation_program_executions WHERE program_id=?`, programID).Scan(&out.CompletedEffects, &out.UncertainEffects, &out.ExecutedChildren, &out.ExecutedReads, &reached, &unsettled)
	if err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE state='not_started'),count(*) FILTER (WHERE state='untaken') FROM invocation_program_nodes WHERE program_id=?`, programID).Scan(&out.StaticNotStarted, &out.StaticUntaken)
	if err != nil {
		return out, err
	}
	out.Effects, err = selectExecutionProjection(ctx, tx, programID, "completed", maximum)
	if err != nil {
		return out, err
	}
	out.Unknown, err = selectExecutionProjection(ctx, tx, programID, "uncertain", maximum)
	if err != nil {
		return out, err
	}
	stopped, err := selectExecutionProjection(ctx, tx, programID, "stopped", 1)
	if err != nil {
		return out, err
	}
	if len(stopped) > 0 {
		out.Stopped = &stopped[0]
	}
	out.OmittedEffects = out.CompletedEffects - len(out.Effects)
	out.OmittedUnknown = out.UncertainEffects - len(out.Unknown)
	var exact bool
	if err = tx.QueryRowContext(ctx, `SELECT source_exact FROM invocation_programs WHERE id=?`, programID).Scan(&exact); err != nil {
		return out, err
	}
	switch {
	case reached > 0:
		out.FreshInvocationLimitation = "reached_execution_requires_specific_remaining_action"
	case unsettled > 0 || out.UncertainEffects > 0 || out.CompletedEffects > 0:
		out.FreshInvocationLimitation = "outcome_requires_owner_reconciliation"
	case !exact:
		out.FreshInvocationLimitation = "exact_source_not_retained"
	case out.Program.State != "rejected":
		out.FreshInvocationLimitation = "program_is_not_a_zero_execution_rejection"
	default:
		out.FreshInvocationReference = out.Program.SourceRef
	}
	return out, tx.Commit()
}

func selectExecutionProjection(ctx context.Context, tx *sql.Tx, program, kind string, maximum int) ([]ExecutionProjection, error) {
	// This selector is host-owned closed data, never a caller-supplied SQL clause.
	var condition string
	switch kind {
	case "completed":
		condition = "e.effect_certainty='completed'"
	case "uncertain":
		condition = "e.effect_certainty IN ('unknown','attempted')"
	case "stopped":
		condition = "(e.host_stop_code<>'' OR e.stop_code<>'' OR e.state IN ('refused','failed','interrupted'))"
	default:
		return nil, fmt.Errorf("unknown execution projection selector")
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.ordinal,n.source_unit_id,n.start_byte,n.end_byte,n.line,n.column_number FROM invocation_program_executions e JOIN invocation_program_nodes n ON n.program_id=e.program_id AND n.node_id=e.node_id WHERE e.program_id=? AND `+condition+` ORDER BY e.ordinal DESC LIMIT ?`, program, maximum)
	if err != nil {
		return nil, err
	}
	type selected struct {
		ordinal    int64
		projection ExecutionProjection
	}
	entries := make([]selected, 0, maximum)
	for rows.Next() {
		var entry selected
		p := &entry.projection
		err = rows.Scan(&entry.ordinal, &p.SourceUnitID, &p.Span.StartByte, &p.Span.EndByte, &p.Span.Line, &p.Span.Column)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		entries = append(entries, entry)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	out := make([]ExecutionProjection, 0, len(entries))
	for _, entry := range entries {
		values, _, err := readExecutions(ctx, tx, program, entry.ordinal-1, 1)
		if err != nil {
			return nil, err
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("selected execution disappeared")
		}
		entry.projection.Receipt = values[0]
		out = append(out, entry.projection)
	}
	return out, nil
}
