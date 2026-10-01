package tools

import (
	"reflect"
	"testing"
)

func TestPruneWaitConditionSchemaPublishesOnlyRoleConditions(t *testing.T) {
	original := map[string]any{"type": "object", "properties": map[string]any{
		"timeout_ms": map[string]any{"type": "integer"},
		"conditions": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "properties": map[string]any{
				"kind": map[string]any{"type": "string", "enum": []any{"next_worker_done", "http_ready", "port_ready"}},
			},
		}},
	}}
	got := pruneWaitConditionSchema(original, []string{"http_ready", "port_ready"})
	properties := got["properties"].(map[string]any)
	conditions := properties["conditions"].(map[string]any)
	items := conditions["items"].(map[string]any)
	itemProperties := items["properties"].(map[string]any)
	kind := itemProperties["kind"].(map[string]any)
	if !reflect.DeepEqual(kind["enum"], []any{"http_ready", "port_ready"}) {
		t.Fatalf("published condition enum = %#v", kind["enum"])
	}
	originalProperties := original["properties"].(map[string]any)
	originalConditions := originalProperties["conditions"].(map[string]any)
	originalItems := originalConditions["items"].(map[string]any)
	originalItemProperties := originalItems["properties"].(map[string]any)
	originalKind := originalItemProperties["kind"].(map[string]any)
	if !reflect.DeepEqual(originalKind["enum"], []any{"next_worker_done", "http_ready", "port_ready"}) {
		t.Fatalf("source schema mutated = %#v", originalKind["enum"])
	}
}

func TestPruneWaitConditionSchemaRemovesConditionsForTimerOnlyRole(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"timeout_ms": map[string]any{"type": "integer"},
		"conditions": map[string]any{"type": "array"},
	}}
	got := pruneWaitConditionSchema(schema, nil)
	properties := got["properties"].(map[string]any)
	if _, exists := properties["conditions"]; exists {
		t.Fatal("timer-only role published conditions")
	}
}
