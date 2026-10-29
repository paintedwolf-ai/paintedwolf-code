package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/lycaon/lycaon/internal/tools/argdiag"
)

// SchemaRequiresValidation reports whether ArgsSchema imposes constraints beyond a bare object type.
func SchemaRequiresValidation(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	typ, _ := schema["type"].(string)
	if typ != "object" {
		return true
	}
	for key, value := range schema {
		if key == "type" {
			continue
		}
		if key == "additionalProperties" && value == true {
			continue
		}
		return true
	}
	return false
}

// ValidateToolArgs checks args against a JSON Schema object when the schema is non-trivial.
func ValidateToolArgs(schema map[string]any, args map[string]any) error {
	if !SchemaRequiresValidation(schema) {
		return nil
	}
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal schema: %w", err)
	}
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}
	return toolcontract.ValidateArgumentInstance(schemaBytes, argsBytes)
}

// ValidateCallArguments checks transport integrity and argument shape before policy reads them.
func ValidateCallArguments(qualifiedName string, args, schema map[string]any, tc ToolContext) *toolrejection.ToolReject {
	if tc.Effects.ArgsTruncated {
		return toolrejection.RejectInvalidArguments("TOOL_ARGS_TRUNCATED", map[string]any{"tool": qualifiedName})
	}
	if tc.Effects.ArgsMalformed {
		return toolrejection.RejectInvalidArguments("TOOL_ARGS_MALFORMED", map[string]any{"tool": qualifiedName})
	}
	if _, err := json.Marshal(args); err != nil {
		return toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"tool": qualifiedName, "reason": "arguments cannot be encoded as JSON",
		})
	}
	if schema != nil {
		if err := ValidateToolArgs(schema, args); err != nil {
			var invalid *jsonschema.ValidationError
			if !errors.As(err, &invalid) {
				return &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, Data: map[string]any{"tool": qualifiedName, "reason": err.Error()}}
			}
			diag := argdiag.Diagnose(schema, args, ValidateToolArgs)
			data := map[string]any{
				"reason": diag.Reason(err, args),
				"tool":   qualifiedName,
			}
			diag.AddFacts(data)
			if required := argdiag.MissingRequiredSummary(invalid, schema, args); required != "" {
				data["missing_required_arguments"] = required
			}
			data["schema_issues"] = argdiag.SchemaIssues(invalid)
			return toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", data)
		}
	}
	return nil
}
