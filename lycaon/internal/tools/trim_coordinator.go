package tools

import (
	"strings"
)

// coordinatorSchemaValueKeys are provider-safe JSON Schema constraints that do
// not contain nested schemas. They are preserved verbatim at every depth.
var coordinatorSchemaValueKeys = []string{
	"description", "enum", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"multipleOf", "minLength", "maxLength", "pattern", "format", "minItems", "maxItems",
	"uniqueItems", "minProperties", "maxProperties", "default", "examples",
}

// TrimCoordinatorToolMeta removes unsupported schema structure while preserving argument constraints and descriptions.
func TrimCoordinatorToolMeta(meta ToolMeta) ToolMeta {
	out := meta
	out.Description = strings.TrimSpace(meta.Description)
	out.ArgsSchema = trimArgsSchema(meta.ArgsSchema)
	return out
}

func trimArgsSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object"}
	}
	out := trimSchemaNode(schema, "object")
	for _, key := range functionParametersRootForbiddenKeys {
		delete(out, key)
	}
	if _, ok := out["type"]; !ok {
		out["type"] = "object"
	}
	return out
}

func trimSchemaNode(schema map[string]any, fallbackType string) map[string]any {
	out := map[string]any{}
	if typ, ok := schemaTypeValue(schema); ok {
		out["type"] = typ
	} else if !hasSchemaCombinator(schema) {
		out["type"] = fallbackType
	}
	for _, key := range coordinatorSchemaValueKeys {
		if val, ok := schema[key]; ok {
			out[key] = val
		}
	}
	if req, ok := copySchemaList(schema["required"]); ok && len(req) > 0 {
		out["required"] = req
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		trimmed := make(map[string]any, len(props))
		for name, raw := range props {
			trimmed[name] = trimPropertySchema(raw)
		}
		out["properties"] = trimmed
	}
	if items, ok := schema["items"].(map[string]any); ok {
		out["items"] = trimSchemaNode(items, "string")
	}
	switch additional := schema["additionalProperties"].(type) {
	case bool:
		out["additionalProperties"] = additional
	case map[string]any:
		out["additionalProperties"] = trimSchemaNode(additional, "string")
	}
	for _, combinator := range []string{"oneOf", "anyOf", "allOf"} {
		if trimmed, ok := trimSchemaBranchList(schema[combinator]); ok {
			out[combinator] = trimmed
		}
	}
	return out
}

func trimSchemaBranchList(raw any) ([]any, bool) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		branch, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, trimSchemaNode(branch, "object"))
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func schemaTypeValue(schema map[string]any) (any, bool) {
	switch t := schema["type"].(type) {
	case string:
		if t != "" {
			return t, true
		}
	case []any:
		if len(t) > 0 {
			return append([]any(nil), t...), true
		}
	}
	return nil, false
}

func hasSchemaCombinator(schema map[string]any) bool {
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if items, ok := schema[key].([]any); ok && len(items) > 0 {
			return true
		}
	}
	return false
}

func trimPropertySchema(raw any) map[string]any {
	prop, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{"type": "string"}
	}
	return trimSchemaNode(prop, "string")
}

func copySchemaList(raw any) ([]any, bool) {
	switch values := raw.(type) {
	case []any:
		return append([]any(nil), values...), true
	case []string:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out, true
	default:
		return nil, false
	}
}
