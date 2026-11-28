package toolschema

import (
	"reflect"
	"testing"
)

func TestProjectionKeepsSelfContainedUnions(t *testing.T) {
	for _, kind := range []string{"anyOf", "oneOf", "allOf"} {
		t.Run(kind, func(t *testing.T) {
			node := map[string]any{kind: []any{map[string]any{"type": "string"}, map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}}}
			if got := projectSchemaNode(node); !reflect.DeepEqual(got, node) {
				t.Fatalf("union changed: %v", got)
			}
		})
	}
}

func TestProjectionBranchAndPropertyShapes(t *testing.T) {
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
	if got := ProjectArgsSchema(nil); got["type"] != "object" {
		t.Fatalf("empty root = %v", got)
	}
}

func TestSharedNarrowingPrimitivesPreserveSourceSchemas(t *testing.T) {
	waitRaw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timeout_ms": map[string]any{"type": "integer"},
			"conditions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []any{"next_worker_done", "http_ready", "port_ready"}},
					},
				},
			},
		},
	}
	narrowedWait := PruneWaitConditionSchema(waitRaw, []string{"http_ready"})
	if !SchemaAcceptsField(narrowedWait, "conditions.kind") {
		t.Fatal("expected narrowed wait to retain conditions.kind")
	}
	timerOnlyWait := PruneWaitConditionSchema(waitRaw, nil)
	if SchemaAcceptsField(timerOnlyWait, "conditions") {
		t.Fatal("timer-only wait schema retained conditions")
	}

	askRaw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt":        map[string]any{"type": "string"},
			"response_type": map[string]any{"type": "string", "enum": []any{"text", "single_choice", "multi_choice", "secret"}},
			"secret":        map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
		},
	}
	narrowedAsk := PruneAskUserSchema(askRaw)
	if SchemaAcceptsField(narrowedAsk, "secret") || !SchemaAcceptsField(askRaw, "secret") {
		t.Fatalf("PruneAskUserSchema mismatch: narrowed=%v raw=%v", narrowedAsk, askRaw)
	}
	rt := narrowedAsk["properties"].(map[string]any)["response_type"].(map[string]any)
	if !reflect.DeepEqual(rt["enum"], []any{"text", "single_choice", "multi_choice"}) {
		t.Fatalf("ask_user response_type enum = %v", rt["enum"])
	}
	if !SchemaAcceptsEnumValue(askRaw, "response_type", "secret") || SchemaAcceptsEnumValue(narrowedAsk, "response_type", "secret") {
		t.Fatalf("SchemaAcceptsEnumValue secret mismatch: raw=%t narrowed=%t",
			SchemaAcceptsEnumValue(askRaw, "response_type", "secret"),
			SchemaAcceptsEnumValue(narrowedAsk, "response_type", "secret"))
	}
	if !SchemaAcceptsEnumValue(narrowedWait, "conditions.kind", "http_ready") || SchemaAcceptsEnumValue(narrowedWait, "conditions.kind", "port_ready") {
		t.Fatal("SchemaAcceptsEnumValue failed on nested array items enum")
	}
	if SchemaAcceptsEnumValue(nil, "response_type", "secret") || SchemaAcceptsEnumValue(askRaw, "missing_field", "secret") {
		t.Fatal("SchemaAcceptsEnumValue accepted nil schema or missing field")
	}
}

func TestStringAdmissionRespectsIntersectingFieldAndRootConstraints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		schema     map[string]any
		wantSecret bool
		wantText   bool
	}{
		{
			name: "field_enum_narrowed_by_allOf_const",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{
						"type":  "string",
						"enum":  []any{"text", "secret"},
						"allOf": []any{map[string]any{"const": "text"}},
					},
				},
			},
			wantSecret: false,
			wantText:   true,
		},
		{
			name: "field_enum_narrowed_by_sibling_const",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{
						"type":  "string",
						"enum":  []any{"text", "secret"},
						"const": "text",
					},
				},
			},
			wantSecret: false,
			wantText:   true,
		},
		{
			name: "field_enum_intersected_with_allOf_admitting_secret",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{
						"type":  "string",
						"enum":  []any{"text", "secret"},
						"allOf": []any{map[string]any{"enum": []any{"single_choice", "secret"}}},
					},
				},
			},
			wantSecret: true,
			wantText:   false,
		},
		{
			name: "root_allOf_narrows_property_enum",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{"type": "string", "enum": []any{"text", "secret"}},
				},
				"allOf": []any{
					map[string]any{
						"properties": map[string]any{
							"response_type": map[string]any{"const": "text"},
						},
					},
				},
			},
			wantSecret: false,
			wantText:   true,
		},
		{
			name: "root_allOf_intersection_admits_secret",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{"type": "string", "enum": []any{"text", "single_choice", "secret"}},
				},
				"allOf": []any{
					map[string]any{
						"properties": map[string]any{
							"response_type": map[string]any{"enum": []any{"text", "secret"}},
						},
					},
					map[string]any{"required": []any{"response_type"}},
				},
			},
			wantSecret: true,
			wantText:   true,
		},
		{
			name: "field_not_keyword_conservatively_withholds",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{
						"type": "string",
						"enum": []any{"text", "secret"},
						"not":  map[string]any{"const": "secret"},
					},
				},
			},
			wantSecret: false,
			wantText:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SchemaAcceptsEnumValue(tc.schema, "response_type", "secret"); got != tc.wantSecret {
				t.Fatalf("SchemaAcceptsEnumValue(secret) = %t, want %t", got, tc.wantSecret)
			}
			if got := SchemaAdmitsStringValue(tc.schema, "response_type", "secret"); got != tc.wantSecret {
				t.Fatalf("SchemaAdmitsStringValue(secret) = %t, want %t", got, tc.wantSecret)
			}
			if got := SchemaAcceptsEnumValue(tc.schema, "response_type", "text"); got != tc.wantText {
				t.Fatalf("SchemaAcceptsEnumValue(text) = %t, want %t", got, tc.wantText)
			}
			if got := SchemaAdmitsStringValue(tc.schema, "response_type", "text"); got != tc.wantText {
				t.Fatalf("SchemaAdmitsStringValue(text) = %t, want %t", got, tc.wantText)
			}
		})
	}
}

func TestStringAdmissionCoversCombinatorsAndStringBounds(t *testing.T) {
	anyOfSchema := map[string]any{
		"type": "object",
		"anyOf": []any{
			map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "fast"}}},
			map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "safe"}}},
		},
	}
	if !SchemaAcceptsEnumValue(anyOfSchema, "mode", "fast") || SchemaAcceptsEnumValue(anyOfSchema, "mode", "other") {
		t.Fatal("root anyOf enum admission mismatch")
	}

	oneOfSchema := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "fast"}}},
			map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "safe"}}},
		},
	}
	if !SchemaAcceptsEnumValue(oneOfSchema, "mode", "safe") || SchemaAcceptsEnumValue(oneOfSchema, "mode", "none") {
		t.Fatal("root oneOf exact-branch admission mismatch")
	}
	overlappingOneOf := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{
				"oneOf": []any{
					map[string]any{"enum": []any{"fast", "safe"}},
					map[string]any{"const": "safe"},
				},
			},
		},
	}
	if SchemaAcceptsEnumValue(overlappingOneOf, "mode", "safe") || !SchemaAcceptsEnumValue(overlappingOneOf, "mode", "fast") {
		t.Fatal("leaf oneOf must reject multi-branch match and accept single-branch match")
	}
	mixedOneOf := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "safe"}}, "required": []any{"extra"}},
			map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "fast"}}},
		},
	}
	if SchemaAcceptsEnumValue(mixedOneOf, "mode", "safe") {
		t.Fatal("mixed root oneOf with required sibling must withhold")
	}

	leafBounds := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tag": map[string]any{
				"type":      []any{"string", "null"},
				"minLength": float64(2),
				"maxLength": 5,
				"pattern":   "^[a-z]+$",
				"anyOf":     []any{map[string]any{"const": "abc"}, map[string]any{"const": "de"}},
			},
			"num": map[string]any{"type": "integer"},
		},
		"additionalProperties": false,
	}
	if !SchemaAcceptsEnumValue(leafBounds, "tag", "abc") || SchemaAdmitsStringValue(leafBounds, "tag", "a") || SchemaAdmitsStringValue(leafBounds, "num", "1") || SchemaAdmitsStringValue(leafBounds, "undeclared", "x") {
		t.Fatal("scalar string bounds or additionalProperties=false mismatch")
	}
}
