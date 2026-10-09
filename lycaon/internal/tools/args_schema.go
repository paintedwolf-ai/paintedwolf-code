package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/lycaon/lycaon/internal/tools/argdiag"
)

var schemaCache sync.Map // string(cache key) -> *jsonschema.Schema

// schemaRequiresValidation reports whether ArgsSchema imposes constraints beyond a bare object type.
func schemaRequiresValidation(schema map[string]any) bool {
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
	if !schemaRequiresValidation(schema) {
		return nil
	}
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal schema: %w", err)
	}
	cacheKey := string(schemaBytes)
	cached, ok := schemaCache.Load(cacheKey)
	var sch *jsonschema.Schema
	if ok {
		sch = cached.(*jsonschema.Schema)
	} else {
		var doc any
		if err := json.Unmarshal(schemaBytes, &doc); err != nil {
			return fmt.Errorf("parse schema: %w", err)
		}
		const schemaURL = "lycaon://tool-args/schema.json"
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(schemaURL, doc); err != nil {
			return fmt.Errorf("compile schema: %w", err)
		}
		sch, err = compiler.Compile(schemaURL)
		if err != nil {
			return fmt.Errorf("compile schema: %w", err)
		}
		schemaCache.Store(cacheKey, sch)
	}
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(argsBytes))
	if err != nil {
		return fmt.Errorf("parse args: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return err
	}
	return nil
}

func mutationRecoveryTool(tool string, addressable []string) string {
	switch strings.TrimSpace(tool) {
	case "edit", "replace_lines", "code_rewrite":
	default:
		return ""
	}
	for _, name := range addressable {
		if name == "write" {
			return "write"
		}
	}
	return ""
}

// RejectInvalidArguments marks an observed argument check independently of its diagnostic.
func RejectInvalidArguments(code string, data map[string]any) *ToolReject {
	return &ToolReject{Code: code, Data: data, ArgumentValidation: true}
}

// ValidateCallArguments checks transport integrity and argument shape before policy reads them.
func ValidateCallArguments(qualifiedName string, args, schema map[string]any, tc ToolContext) *ToolReject {
	if tc.ArgsTruncated {
		return RejectInvalidArguments("TOOL_ARGS_TRUNCATED", map[string]any{"tool": qualifiedName})
	}
	if tc.ArgsMalformed {
		return RejectInvalidArguments("TOOL_ARGS_MALFORMED", map[string]any{"tool": qualifiedName})
	}
	if _, err := json.Marshal(args); err != nil {
		return RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"tool": qualifiedName, "reason": "arguments cannot be encoded as JSON",
		})
	}
	if schema != nil {
		if err := ValidateToolArgs(schema, args); err != nil {
			var invalid *jsonschema.ValidationError
			if !errors.As(err, &invalid) {
				return &ToolReject{Code: ToolOwnerFailedCode, Data: map[string]any{"tool": qualifiedName, "reason": err.Error()}}
			}
			diag := argdiag.Diagnose(schema, args, ValidateToolArgs)
			data := map[string]any{
				"reason": diag.Reason(err, args),
				"tool":   qualifiedName,
			}
			diag.AddFacts(data)
			data["schema_issues"] = argdiag.SchemaIssues(invalid)
			if sibling := mutationRecoveryTool(qualifiedName, tc.TurnToolPlan.AddressableNames()); sibling != "" {
				data["suggested_tool"] = sibling
			}
			return RejectInvalidArguments("TOOL_ARGS_INVALID", data)
		}
	}
	return nil
}
