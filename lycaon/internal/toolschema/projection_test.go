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
}
