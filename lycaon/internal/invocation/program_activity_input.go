package invocation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// readActivityInput decodes an execution's presentation fields. display, when
// set, renders recorded host paths as workspace paths.
func readActivityInput(ctx context.Context, tx *sql.Tx, receipt api.CommandExecutionReceipt, display func(string) string) (activityInput, error) {
	input := activityInput{receipt: receipt}
	var body []byte
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT e.operation_id,e.scope_json,s.detail_ref,n.start_byte,n.end_byte,
 COALESCE((SELECT json_extract(c.value,'$.family') FROM json_each(d.definition_json,'$.Commands') c WHERE json_extract(c.value,'$.name')=e.native_command LIMIT 1),''),
 COALESCE(o.inline_digest,''),o.inline_body,COALESCE(parent.operation_id='program |',0)
 FROM invocation_program_executions e
 JOIN invocation_program_definitions d ON d.program_id=e.program_id AND d.definition_digest=e.definition_digest
 JOIN invocation_program_nodes n ON n.program_id=e.program_id AND n.node_id=e.node_id
 JOIN invocation_program_sources s ON s.program_id=n.program_id AND s.source_unit_id=n.source_unit_id
 LEFT JOIN invocation_program_executions parent ON parent.program_id=e.program_id AND parent.id=e.parent_execution_id
 LEFT JOIN invocation_program_outputs o ON o.program_id=e.program_id AND o.reference=CASE WHEN e.reviewed_arguments_ref<>'' THEN e.reviewed_arguments_ref ELSE e.arguments_ref END
 WHERE e.program_id=? AND e.id=?`, receipt.ProgramID, receipt.ID).Scan(&input.operation, &input.scope, &input.span.SourceRef, &input.span.StartByte, &input.span.EndByte, &input.family, &digest, &body, &input.pipeline)
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
	if path, ok := redirectDisplayPath(receipt); ok {
		if input.arguments == nil {
			input.arguments = map[string]any{}
		}
		input.arguments["path_targets"] = map[string]any{"read": []any{}, "write": []any{boundedActivityText(path)}}
	}
	if display != nil && input.arguments != nil {
		displayActivityPaths(input.arguments, display)
	}
	return input, err
}

// displayActivityPaths rewrites the path fields subjects read. Reviewed inputs
// keep resolved host paths; activities show workspace paths.
func displayActivityPaths(arguments map[string]any, display func(string) string) {
	if path, ok := arguments["path"].(string); ok {
		arguments["path"] = display(path)
	}
	targets, _ := arguments["path_targets"].(map[string]any)
	for _, role := range []string{"read", "write"} {
		paths, _ := targets[role].([]any)
		for i, path := range paths {
			if value, ok := path.(string); ok {
				paths[i] = display(value)
			}
		}
	}
}

// activityRedirect reports whether an execution is an output redirect.
func activityRedirect(input activityInput) bool {
	_, ok := redirectDisplayPath(input.receipt)
	return ok
}

// redirectDisplayPath is the workspace path an output redirect wrote. Its
// reviewed inputs hold the resolved path; the host records its command as the
// bound operator and the operator followed by the display path.
func redirectDisplayPath(receipt api.CommandExecutionReceipt) (string, bool) {
	if receipt.EffectMode != "publication" || !toolcontract.OutputRedirectOperator(receipt.NativeCommand) {
		return "", false
	}
	path, ok := strings.CutPrefix(receipt.Command, receipt.NativeCommand+" ")
	return path, ok && path != ""
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
	for _, name := range []string{"command", "cwd", "path", "pattern", "query", "url", "message", "task", "focus", "bundle"} {
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
	options, _ := arguments["options"].([]any)
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if optionFlag(option, "--focus") {
			if value := optionValue(option); value != "" {
				result["focus"] = boundedActivityText(value)
			}
		}
		if optionFlag(option, "--bundle") {
			if value := optionValue(option); value != "" {
				result["bundle"] = boundedActivityText(value)
			}
		}
		role := stringArgument(option, "value_role")
		if role != "pattern" && role != "glob" && role != "regex" && role != "query" {
			continue
		}
		values, _ := option["values"].([]any)
		if len(values) > 0 {
			if value, ok := values[0].(string); ok {
				result["query"] = boundedActivityText(value)
				break
			}
		}
	}
	result["path_targets"] = targets
	return result, nil
}

// optionFlag reports whether the restored option spells flag, by spellings or name.
func optionFlag(option map[string]any, flag string) bool {
	if spellings, ok := option["spellings"].([]any); ok {
		for _, raw := range spellings {
			if spelling, _ := raw.(string); spelling == flag {
				return true
			}
		}
	}
	return stringArgument(option, "name") == flag
}

func optionValue(option map[string]any) string {
	values, _ := option["values"].([]any)
	if len(values) == 0 {
		return ""
	}
	value, _ := values[0].(string)
	return value
}
