package invocation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func readActivityInput(ctx context.Context, tx *sql.Tx, receipt api.CommandExecutionReceipt) (activityInput, error) {
	input := activityInput{receipt: receipt}
	var body []byte
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT e.operation_id,e.scope_json,s.detail_ref,n.start_byte,n.end_byte,
 COALESCE((SELECT json_extract(c.value,'$.family') FROM json_each(d.definition_json,'$.Commands') c WHERE json_extract(c.value,'$.name')=e.native_command LIMIT 1),''),
 COALESCE(o.inline_digest,''),o.inline_body,COALESCE(parent.operation_id='program |',0),COALESCE(pn.start_byte,0)
 FROM invocation_program_executions e
 JOIN invocation_program_definitions d ON d.program_id=e.program_id AND d.definition_digest=e.definition_digest
 JOIN invocation_program_nodes n ON n.program_id=e.program_id AND n.node_id=e.node_id
 JOIN invocation_program_sources s ON s.program_id=n.program_id AND s.source_unit_id=n.source_unit_id
 LEFT JOIN invocation_program_executions parent ON parent.program_id=e.program_id AND parent.id=e.parent_execution_id
 LEFT JOIN invocation_program_nodes pn ON pn.program_id=parent.program_id AND pn.node_id=parent.node_id
 LEFT JOIN invocation_program_outputs o ON o.program_id=e.program_id AND o.reference=CASE WHEN e.reviewed_arguments_ref<>'' THEN e.reviewed_arguments_ref ELSE e.arguments_ref END
 WHERE e.program_id=? AND e.id=?`, receipt.ProgramID, receipt.ID).Scan(&input.operation, &input.scope, &input.span.SourceRef, &input.span.StartByte, &input.span.EndByte, &input.family, &digest, &body, &input.pipeline, &input.pipelineStart)
	if err != nil {
		return input, err
	}
	if len(body) > 0 {
		arguments, err := activityArguments(ctx, body, digest)
		if err != nil {
			return input, err
		}
		input.arguments = arguments
	}
	if receipt.Lifecycle == "read_only" && activityPresentation(input).label == "Read source" && !activityNeedsAttention(receipt) {
		identity := stringArgument(input.arguments, "activity_path_identity")
		if identity != "" {
			sum := sha256.Sum256([]byte(input.scope + "|" + identity))
			input.mergeKey = hex.EncodeToString(sum[:])
		}
	}
	return input, err
}

type activityReadBudget struct{ used int64 }

func (b *activityReadBudget) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n < 0 || n > 16<<20 || b.used+n > 16<<20 {
		return nil, fmt.Errorf("activity input exceeds read budget")
	}
	b.used += n
	return func() { b.used -= n }, nil
}

func activityArguments(ctx context.Context, body []byte, digest string) (map[string]any, error) {
	budget := &activityReadBudget{}
	value, release, err := toolcontract.RestoreReviewedOperationInputs(ctx, budget, toolcontract.LosslessOperationArgumentsV1, digest, string(body))
	if err != nil {
		return nil, err
	}
	defer release()
	arguments, releaseArguments, err := value.Arguments(ctx, budget)
	if err != nil {
		return nil, err
	}
	defer releaseArguments()
	// Keep only bounded presentation fields after releasing the inspection value.
	result := map[string]any{}
	if paths := activityPaths(arguments, "read"); len(paths) == 1 {
		sum := sha256.Sum256([]byte(paths[0]))
		result["activity_path_identity"] = hex.EncodeToString(sum[:])
	}
	for _, name := range []string{"command", "cwd", "path"} {
		if value := stringArgument(arguments, name); value != "" {
			result[name] = boundedActivityText(value)
		}
	}
	targets := map[string]any{}
	for _, role := range []string{"read", "write"} {
		paths := activityPaths(arguments, role)
		if len(paths) > 16 {
			paths = paths[:16]
		}
		values := make([]any, 0, len(paths))
		for _, path := range paths {
			values = append(values, boundedActivityText(path))
		}
		targets[role] = values
	}
	result["path_targets"] = targets
	return result, nil
}
