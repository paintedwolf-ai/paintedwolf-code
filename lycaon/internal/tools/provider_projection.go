package tools

import (
	"reflect"
	"strings"
)

// Scalar constraints survive projection at every depth.
var providerSchemaValueKeys = []string{
	"description", "enum", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"multipleOf", "minLength", "maxLength", "pattern", "format", "minItems", "maxItems",
	"uniqueItems", "minProperties", "maxProperties", "default", "examples",
}

// ProjectProviderToolMeta derives generation guidance without changing execution schemas.
func ProjectProviderToolMeta(meta ToolMeta) ToolMeta {
	out := meta
	out.Description = strings.TrimSpace(meta.Description)
	out.ArgsSchema = projectArgsSchema(meta.ArgsSchema)
	return out
}

// PruneCommandReadOnlySchema removes the mutating, external-execution and
// capture parameters from the command schema when a profile or surface limits
// command to read-only native utilities.
func PruneCommandReadOnlySchema(schema map[string]any) map[string]any {
	out := projectArgsSchema(schema)
	props, _ := out["properties"].(map[string]any)
	for _, key := range []string{
		"capability_request",
		"secret_use",
		"snapshot_capture",
		"socks_proxy",
		"terminal_capture",
		"verification",
	} {
		delete(props, key)
	}
	return out
}

func projectArgsSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object"}
	}
	out := projectSchemaNode(schema)
	for _, key := range functionParametersRootForbiddenKeys {
		delete(out, key)
	}
	if _, ok := out["type"]; !ok {
		out["type"] = "object"
	}
	return out
}

func projectSchemaNode(schema map[string]any) map[string]any {
	out := map[string]any{}
	if typ, ok := schemaTypeValue(schema); ok {
		out["type"] = typ
	}
	for _, key := range providerSchemaValueKeys {
		if val, ok := schema[key]; ok {
			out[key] = val
		}
	}
	if required := projectRequiredProperties(schema); len(required) > 0 {
		out["required"] = required
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		trimmed := make(map[string]any, len(props))
		for name, raw := range props {
			trimmed[name] = projectPropertySchema(raw)
		}
		out["properties"] = trimmed
	}
	if items, ok := schema["items"].(map[string]any); ok && schema["prefixItems"] == nil {
		out["items"] = projectSchemaNode(items)
	}
	// Pattern properties are omitted, so their values cannot become additional.
	if schema["patternProperties"] == nil {
		switch additional := schema["additionalProperties"].(type) {
		case bool:
			out["additionalProperties"] = additional
		case map[string]any:
			out["additionalProperties"] = projectSchemaNode(additional)
		}
	}
	for _, combinator := range []string{"oneOf", "anyOf", "allOf"} {
		if trimmed, ok := projectSchemaBranches(combinator, schema[combinator]); ok {
			out[combinator] = trimmed
		}
	}
	return out
}

func projectSchemaBranches(combinator string, raw any) ([]any, bool) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		branch, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		projected := projectSchemaNode(branch)
		_, typed := schemaTypeValue(projected)
		if !typed && !hasSchemaCombinator(projected) {
			// Dropping one alternative would exclude valid arguments.
			if combinator != "allOf" {
				return nil, false
			}
			continue
		}
		// Widened exclusive branches can overlap and reject valid arguments.
		if combinator == "oneOf" && !reflect.DeepEqual(branch, projected) {
			return nil, false
		}
		out = append(out, projected)
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

func projectPropertySchema(raw any) map[string]any {
	prop, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return projectSchemaNode(prop)
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

func projectRequiredProperties(schema map[string]any) []any {
	required, _ := copySchemaList(schema["required"])
	props, _ := schema["properties"].(map[string]any)
	out := make([]any, 0, len(required))
	for _, raw := range required {
		if name, ok := raw.(string); ok {
			if _, declared := props[name]; declared {
				out = append(out, name)
			}
		}
	}
	return out
}
