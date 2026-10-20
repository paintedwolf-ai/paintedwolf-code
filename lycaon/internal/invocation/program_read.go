package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/pkg/api"
)

var ErrProgramNotFound = errors.New("program receipt not found")

type programPosition struct {
	Execution int64
	Node      string
}

var programPages = pagecursor.For[programPosition]("invocation-program-receipts")

// ProgramReader exposes bounded durable facts without hydrating retained bodies.
type ProgramReader interface {
	ReadProgramPage(context.Context, string, string, string, int) (api.CommandProgramReceiptPage, error)
}

func (r *SQLRecorder) ReadProgramPage(ctx context.Context, sessionID, programID, cursor string, limit int) (api.CommandProgramReceiptPage, error) {
	out := api.CommandProgramReceiptPage{Executions: []api.CommandExecutionReceipt{}, Nodes: []api.CommandProgramNode{}}
	if r == nil || r.database == nil {
		return out, fmt.Errorf("invocation recorder is not configured")
	}
	if limit < 1 || limit > 100 {
		return out, fmt.Errorf("program page limit must be 1..100")
	}
	scope := pagecursor.Scope(sessionID, programID)
	position := programPosition{}
	var err error
	if cursor != "" {
		position, err = programPages.Decode(cursor, scope)
		if err != nil {
			return out, err
		}
	}
	// A writer transaction gives the returned parent, child counts and page one
	// coherent snapshot; no post-commit read decides an accepted transition.
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	out.Program, err = readProgram(ctx, tx, sessionID, programID)
	if err != nil {
		return out, err
	}
	out.Progress, err = readProgramProgress(ctx, tx, programID)
	if err != nil {
		return out, err
	}
	next := position
	out.Executions, next.Execution, err = readExecutions(ctx, tx, programID, position.Execution, limit)
	if err != nil {
		return out, err
	}
	out.Nodes, next.Node, err = readProgramNodes(ctx, tx, programID, position.Node, limit)
	if err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND ordinal>?`, programID, next.Execution).Scan(&out.ExecutionsOmitted); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_nodes WHERE program_id=? AND node_id>?`, programID, next.Node).Scan(&out.NodesOmitted); err != nil {
		return out, err
	}
	if out.ExecutionsOmitted > 0 || out.NodesOmitted > 0 {
		out.NextCursor, err = programPages.Encode(scope, next)
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}

func readProgram(ctx context.Context, tx *sql.Tx, session, program string) (api.CommandProgramReceipt, error) {
	var out api.CommandProgramReceipt
	var status sql.NullInt64
	var started string
	var settled sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT p.id,p.parent_receipt_id,p.language_version,p.catalog_digest,p.source_digest,p.source_ref,p.state,p.status_code,p.stop_code,p.detail_ref,p.progress_state,p.progress_ref,p.started_at,p.settled_at FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE p.id=? AND r.session_id=?`, program, session).Scan(&out.ID, &out.ParentReceiptID, &out.LanguageVersion, &out.CatalogDigest, &out.SourceDigest, &out.SourceRef, &out.State, &status, &out.StopCode, &out.DetailRef, &out.ProgressState, &out.ProgressRef, &started, &settled)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrProgramNotFound
	}
	if err != nil {
		return out, err
	}
	out.StatusCode = optionalStatus(status)
	out.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return out, err
	}
	if settled.Valid {
		at, parseErr := time.Parse(time.RFC3339Nano, settled.String)
		if parseErr != nil {
			return out, parseErr
		}
		out.SettledAt = &at
	}
	return out, nil
}

func readExecutions(ctx context.Context, tx *sql.Tx, program string, after int64, limit int) ([]api.CommandExecutionReceipt, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,program_id,node_id,parent_execution_id,owner,lifecycle,definition_digest,arguments_digest,reviewed_arguments_digest,reviewed_arguments_ref,native_command,effect_mode,effect_classes_json,arguments_ref,canonical,state,owner_started,owner_invoked,status_code,stop_code,host_stop_code,complete,publication_eligible,output_publishable,effect_certainty,evidence_kind,evidence_ref,owner_ref,recovery_ref,held_version_id,accepted_revision,published_revision,detail_ref,approval_ref,ordinal FROM invocation_program_executions WHERE program_id=? AND ordinal>? ORDER BY ordinal LIMIT ?`, program, after, limit)
	if err != nil {
		return nil, after, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]api.CommandExecutionReceipt, 0, limit)
	for rows.Next() {
		var value api.CommandExecutionReceipt
		var status sql.NullInt64
		var classes string
		err = rows.Scan(&value.ID, &value.ProgramID, &value.NodeID, &value.ParentExecutionID, &value.Owner, &value.Lifecycle, &value.DefinitionDigest, &value.ArgumentsDigest, &value.ReviewedArgumentsDigest, &value.ReviewedArgumentsRef, &value.NativeCommand, &value.EffectMode, &classes, &value.ArgumentsRef, &value.Command, &value.State, &value.OwnerStarted, &value.OwnerInvoked, &status, &value.StopCode, &value.HostStopCode, &value.Complete, &value.PublicationEligible, &value.OutputPublishable, &value.EffectCertainty, &value.EvidenceKind, &value.EvidenceRef, &value.OwnerRef, &value.RecoveryRef, &value.HeldVersionID, &value.AcceptedRevision, &value.PublishedRevision, &value.DetailRef, &value.ApprovalRef, &after)
		if err != nil {
			return nil, after, err
		}
		if err = json.Unmarshal([]byte(classes), &value.EffectClasses); err != nil {
			return nil, after, err
		}
		value.StatusCode = optionalStatus(status)
		out = append(out, value)
	}
	return out, after, rows.Err()
}

func readProgramNodes(ctx context.Context, tx *sql.Tx, program, after string, limit int) ([]api.CommandProgramNode, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,source_unit_id,start_byte,end_byte,line,column_number,state FROM invocation_program_nodes WHERE program_id=? AND node_id>? ORDER BY node_id LIMIT ?`, program, after, limit)
	if err != nil {
		return nil, after, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]api.CommandProgramNode, 0, limit)
	for rows.Next() {
		var node api.CommandProgramNode
		if err = rows.Scan(&node.ID, &node.SourceUnitID, &node.StartByte, &node.EndByte, &node.Line, &node.Column, &node.State); err != nil {
			return nil, after, err
		}
		after = node.ID
		out = append(out, node)
	}
	return out, after, rows.Err()
}

func optionalStatus(in sql.NullInt64) *int {
	if !in.Valid {
		return nil
	}
	status := int(in.Int64)
	return &status
}

func readProgramProgress(ctx context.Context, tx *sql.Tx, programID string) (*api.CommandProgramProgressReceipt, error) {
	var out api.CommandProgramProgressReceipt
	err := tx.QueryRowContext(ctx, `SELECT execution_id,program_id,root_session_id,state,owner_invoked,arguments_digest,arguments_ref,detail_ref FROM invocation_program_progress WHERE program_id=?`, programID).Scan(&out.ExecutionID, &out.ProgramID, &out.RootSessionID, &out.State, &out.OwnerInvoked, &out.ArgumentsDigest, &out.ArgumentsRef, &out.DetailRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
