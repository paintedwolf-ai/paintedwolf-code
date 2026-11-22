package toolschema

import (
	"reflect"
	"slices"
	"strings"
)

var functionParametersRootForbiddenKeys = []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"}

var providerSchemaValueKeys = []string{
	"description", "enum", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"multipleOf", "minLength", "maxLength", "pattern", "format", "minItems", "maxItems",
	"uniqueItems", "minProperties", "maxProperties", "default", "examples",
}

// ProjectOptions specifies per-turn narrowing inputs for ProjectToolArgsSchema.
type ProjectOptions struct {
	CommandReadOnly     bool
	PruneWaitConditions bool
	WaitConditions      []string
	AskMode             bool
}

// ProjectArgsSchema projects a raw tool schema into its provider-bound request schema.
func ProjectArgsSchema(schema map[string]any) map[string]any {
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

// PruneCommandReadOnlySchema projects schema and strips command parameters
// unavailable when command is restricted to read-only native utilities.
func PruneCommandReadOnlySchema(schema map[string]any) map[string]any {
	out := ProjectArgsSchema(schema)
	props, _ := out["properties"].(map[string]any)
	for _, key := range commandReadOnlyPrunedFields {
		delete(props, key)
	}
	return out
}

// PruneWaitConditionSchema narrows wait's conditions property to allowed kinds.
func PruneWaitConditionSchema(schema map[string]any, allowed []string) map[string]any {
	cloned := cloneSchemaMap(schema)
	properties, _ := cloned["properties"].(map[string]any)
	if len(allowed) == 0 {
		delete(properties, "conditions")
		return cloned
	}
	conditions, _ := properties["conditions"].(map[string]any)
	items, _ := conditions["items"].(map[string]any)
	itemProperties, _ := items["properties"].(map[string]any)
	kind, _ := itemProperties["kind"].(map[string]any)
	if kind != nil {
		values := make([]any, 0, len(allowed))
		for _, value := range allowed {
			values = append(values, value)
		}
		kind["enum"] = values
	}
	return cloned
}

// PruneAskUserSchema narrows ask_user for Ask-mode turns by removing secret prompts.
func PruneAskUserSchema(schema map[string]any) map[string]any {
	cloned := cloneSchemaMap(schema)
	properties, _ := cloned["properties"].(map[string]any)
	if response, ok := properties["response_type"].(map[string]any); ok {
		response["enum"] = []any{"text", "single_choice", "multi_choice"}
		response["description"] = "Kind of response. Defaults to text."
	}
	delete(properties, "secret")
	return cloned
}

// ProjectToolArgsSchema runs the production schema narrowing pipeline for toolName.
func ProjectToolArgsSchema(toolName string, rawSchema map[string]any, opts ProjectOptions) map[string]any {
	cur := rawSchema
	switch strings.TrimSpace(toolName) {
	case "command":
		if opts.CommandReadOnly {
			cur = PruneCommandReadOnlySchema(cur)
		}
	case "wait":
		if opts.PruneWaitConditions {
			cur = PruneWaitConditionSchema(cur, opts.WaitConditions)
		}
	case "ask_user":
		if opts.AskMode {
			cur = PruneAskUserSchema(cur)
		}
	}
	return ProjectArgsSchema(cur)
}

// SchemaAcceptsField reports whether an already-projected request schema
// accepts fieldPath (e.g. "capability_request.host_resources", "secret_use").
func SchemaAcceptsField(projected map[string]any, fieldPath string) bool {
	fieldPath = strings.TrimSpace(fieldPath)
	if fieldPath == "" || projected == nil {
		return false
	}
	_, ok := findFieldSchema(projected, "", fieldPath, 0)
	return ok
}

// SchemaAcceptsEnumValue reports whether projected admits fieldPath and string enum value.
func SchemaAcceptsEnumValue(projected map[string]any, fieldPath, value string) bool {
	fieldPath = strings.TrimSpace(fieldPath)
	if fieldPath == "" || projected == nil {
		return false
	}
	prop, ok := findFieldSchema(projected, "", fieldPath, 0)
	if !ok {
		return false
	}
	rawEnum, hasEnum := prop["enum"]
	if !hasEnum {
		return true
	}
	switch vals := rawEnum.(type) {
	case []string:
		return slices.Contains(vals, value)
	case []any:
		for _, item := range vals {
			if s, isStr := item.(string); isStr && s == value {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func findFieldSchema(schema map[string]any, prefix, target string, depth int) (map[string]any, bool) {
	if schema == nil || depth > argFieldDepthMax {
		return nil, false
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if found, ok := findFieldSchema(items, prefix, target, depth+1); ok {
			return found, true
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		if strings.TrimSpace(name) == "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		child, ok := raw.(map[string]any)
		if path == target {
			return child, ok && child != nil
		}
		if ok && strings.HasPrefix(target, path+".") {
			if found, ok := findFieldSchema(child, path, target, depth+1); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func cloneSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneSchemaValue(v)
	}
	return out
}

func cloneSchemaValue(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return cloneSchemaMap(tv)
	case []any:
		cp := make([]any, len(tv))
		for i, elem := range tv {
			cp[i] = cloneSchemaValue(elem)
		}
		return cp
	default:
		return v
	}
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
			if combinator != "allOf" {
				return nil, false
			}
			continue
		}
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
