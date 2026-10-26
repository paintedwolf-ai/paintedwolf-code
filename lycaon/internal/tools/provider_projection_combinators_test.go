package tools

import (
	"reflect"
	"testing"
)

func TestProviderProjectionRelationalConstraints(t *testing.T) {
	for _, kind := range []string{"anyOf", "oneOf", "allOf"} {
		t.Run(kind, func(t *testing.T) {
			schema := map[string]any{"type": "object", "properties": map[string]any{
				"history": map[string]any{"type": "object", "properties": map[string]any{
					"ordinal": map[string]any{"type": "integer"}, "id": map[string]any{"type": "string"},
				}, kind: []any{map[string]any{"required": []any{"ordinal"}}, map[string]any{"required": []any{"id"}}}},
			}}
			projected := ProjectProviderToolMeta(ToolMeta{ArgsSchema: schema}).ArgsSchema
			history := schemaMap(t, schemaProperties(t, projected)["history"])
			if _, kept := history[kind]; kept {
				t.Fatalf("relational %s survived: %v", kind, history)
			}
			if len(schemaProperties(t, history)) != 2 {
				t.Fatalf("history properties = %v", history)
			}
			original := schemaMap(t, schemaProperties(t, schema)["history"])
			if _, kept := original[kind]; !kept {
				t.Fatal("projection mutated execution schema")
			}
		})
	}
}

func TestProviderProjectionDoesNotNarrowAlternatives(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		values []any
	}{
		{"mixed alternatives", map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"minimum": 2}}}, []any{"ok", 3, true}},
		{"exclusive widening", map[string]any{"oneOf": []any{map[string]any{"type": "string", "const": "a"}, map[string]any{"type": "string", "const": "b"}}}, []any{"a", "b"}},
		{"nested alternatives", map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"anyOf": []any{map[string]any{"required": []any{"id"}}}}}}, []any{"ok", map[string]any{"id": 1}}},
		{"conditional", map[string]any{"allOf": []any{map[string]any{"if": map[string]any{"type": "object"}, "then": map[string]any{"required": []any{"id"}}}}}, []any{true, map[string]any{"id": 1}}},
		{"pattern properties", map[string]any{"type": "object", "patternProperties": map[string]any{"^x": map[string]any{"type": "integer"}}, "additionalProperties": false}, []any{map[string]any{"x": 1}}},
		{"untyped property", map[string]any{}, []any{true, 1, "ok", map[string]any{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := map[string]any{"type": "object", "properties": map[string]any{"value": tc.schema}}
			projected := ProjectProviderToolMeta(ToolMeta{ArgsSchema: full}).ArgsSchema
			for _, value := range tc.values {
				args := map[string]any{"value": value}
				if err := ValidateToolArgs(full, args); err != nil {
					t.Fatalf("invalid fixture: %v", err)
				}
				if err := ValidateToolArgs(projected, args); err != nil {
					t.Fatalf("projection excluded %v: %v", value, err)
				}
			}
		})
	}
}

func TestProviderProjectionKeepsSelfContainedUnions(t *testing.T) {
	for _, kind := range []string{"anyOf", "oneOf", "allOf"} {
		t.Run(kind, func(t *testing.T) {
			node := map[string]any{kind: []any{map[string]any{"type": "string"}, map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}}}
			if got := projectSchemaNode(node); !reflect.DeepEqual(got, node) {
				t.Fatalf("union changed: %v", got)
			}
		})
	}
}

func TestProviderProjectionBranchAndPropertyShapes(t *testing.T) {
	for _, raw := range []any{nil, []any{}, []any{true}} {
		if got, ok := projectSchemaBranches("anyOf", raw); ok {
			t.Fatalf("unsupported branches survived: %v", got)
		}
	}
	if got := projectPropertySchema(true); len(got) != 0 {
		t.Fatalf("boolean schema invented constraints: %v", got)
	}
	node := map[string]any{"type": "object", "required": []string{"declared", "elsewhere"}, "properties": map[string]any{"declared": map[string]any{"type": "string"}}}
	if got := projectSchemaNode(node)["required"]; !reflect.DeepEqual(got, []any{"declared"}) {
		t.Fatalf("required = %v", got)
	}
	if got := ProjectProviderToolMeta(ToolMeta{}).ArgsSchema; got["type"] != "object" {
		t.Fatalf("empty root = %v", got)
	}
}
