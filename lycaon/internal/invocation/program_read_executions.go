package invocation

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lycaon/lycaon/pkg/api"
)

func readExecutions(ctx context.Context, tx *sql.Tx, program string, after int64, limit int, selection executionSelection) ([]api.CommandExecutionReceipt, int64, error) {
	args := append([]any{program, after}, selection.args...)
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.program_id,e.node_id,e.parent_execution_id,d.owner,d.lifecycle,e.definition_digest,e.arguments_representation,e.arguments_digest,e.reviewed_arguments_representation,e.reviewed_arguments_digest,e.reviewed_arguments_ref,e.native_command,e.effect_mode,e.effect_classes_json,e.arguments_ref,e.canonical,e.state,e.owner_started,e.owner_invoked,e.status_code,e.stop_code,e.host_stop_code,e.complete,e.publication_eligible,e.output_publishable,e.effect_certainty,e.evidence_kind,e.evidence_ref,e.owner_ref,e.recovery_ref,e.held_version_id,e.accepted_revision,e.published_revision,e.detail_ref,e.approval_ref,e.job_handle,e.conflict_path,e.conflict_expected_revision,e.conflict_observed_revision,e.ordinal FROM invocation_program_executions e JOIN invocation_program_definitions d ON d.program_id=e.program_id AND d.definition_digest=e.definition_digest WHERE e.program_id=? AND e.ordinal>? `+selection.sql+` ORDER BY e.ordinal LIMIT ?`, args...)
	if err != nil {
		return nil, after, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]api.CommandExecutionReceipt, 0, limit)
	for rows.Next() {
		var value api.CommandExecutionReceipt
		var status sql.NullInt64
		var classes string
		var conflict api.CommandRevisionConflict
		err = rows.Scan(&value.ID, &value.ProgramID, &value.NodeID, &value.ParentExecutionID, &value.Owner, &value.Lifecycle, &value.DefinitionDigest, &value.ArgumentsRepresentation, &value.ArgumentsDigest, &value.ReviewedArgumentsRepresentation, &value.ReviewedArgumentsDigest, &value.ReviewedArgumentsRef, &value.NativeCommand, &value.EffectMode, &classes, &value.ArgumentsRef, &value.Command, &value.State, &value.OwnerStarted, &value.OwnerInvoked, &status, &value.StopCode, &value.HostStopCode, &value.Complete, &value.PublicationEligible, &value.OutputPublishable, &value.EffectCertainty, &value.EvidenceKind, &value.EvidenceRef, &value.OwnerRef, &value.RecoveryRef, &value.HeldVersionID, &value.AcceptedRevision, &value.PublishedRevision, &value.DetailRef, &value.ApprovalRef, &value.JobHandle, &conflict.Path, &conflict.ExpectedRevision, &conflict.ObservedRevision, &after)
		if err != nil {
			return nil, after, err
		}
		if err := json.Unmarshal([]byte(classes), &value.EffectClasses); err != nil {
			return nil, after, err
		}
		value.StatusCode = optionalStatus(status)
		if conflict.Path != "" {
			value.RevisionConflict = &conflict
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		return nil, after, err
	}
	if err := rows.Close(); err != nil {
		return nil, after, err
	}
	return out, after, nil
}

// readExecutionOutputs attaches each child's latest retained stdout and stderr
// handle with its stored screened preview. The body stays behind its handle.
func readExecutionOutputs(ctx context.Context, tx *sql.Tx, program string, executions []api.CommandExecutionReceipt) error {
	for i := range executions {
		outputs, err := readExecutionOutput(ctx, tx, program, executions[i].ID)
		if err != nil {
			return err
		}
		executions[i].Outputs = outputs
	}
	return nil
}

func readExecutionOutput(ctx context.Context, tx *sql.Tx, program, execution string) ([]api.CommandExecutionOutput, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.stream,o.reference,json_extract(o.blob_json,'$.Size'),o.complete,o.redacted,o.preview,o.preview_truncated FROM invocation_program_outputs o WHERE o.program_id=? AND o.execution_id=? AND o.stream IN ('stdout','stderr') AND NOT EXISTS (SELECT 1 FROM invocation_program_outputs later WHERE later.program_id=o.program_id AND later.execution_id=o.execution_id AND later.stream=o.stream AND (later.created_at>o.created_at OR (later.created_at=o.created_at AND later.id>o.id))) ORDER BY o.stream`, program, execution)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var outputs []api.CommandExecutionOutput
	for rows.Next() {
		var output api.CommandExecutionOutput
		if err := rows.Scan(&output.Stream, &output.Ref, &output.SizeBytes, &output.Complete, &output.Redacted, &output.Preview, &output.PreviewTruncated); err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return outputs, rows.Close()
}
