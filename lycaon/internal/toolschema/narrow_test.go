package toolschema

import (
	"reflect"
	"testing"
)

func askUserSchema(enum ...any) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"response_type": map[string]any{"type": "string", "enum": enum, "description": "declared description"},
			"secret":        map[string]any{"type": "object"},
		},
	}
}

func TestPruneAskUserSchemaFiltersSecretFromTheDeclaredEnum(t *testing.T) {
	raw := askUserSchema("text", "secret", "rating")
	got := PruneAskUserSchema(raw)
	response := got["properties"].(map[string]any)["response_type"].(map[string]any)
	if !reflect.DeepEqual(response["enum"], []any{"text", "rating"}) || response["description"] != "declared description" {
		t.Fatalf("response_type = %v, want declared values without secret and the declared description", response)
	}
	if !reflect.DeepEqual(raw["properties"].(map[string]any)["response_type"].(map[string]any)["enum"], []any{"text", "secret", "rating"}) {
		t.Fatal("pruning mutated the raw schema")
	}
}

func TestAskUserAcceptsSecretNeedsTheFieldAndTheEnumValue(t *testing.T) {
	withoutField := askUserSchema("text", "secret")
	delete(withoutField["properties"].(map[string]any), "secret")
	unconstrained := askUserSchema()
	delete(unconstrained["properties"].(map[string]any)["response_type"].(map[string]any), "enum")
	for name, tc := range map[string]struct {
		schema map[string]any
		want   bool
	}{
		"declared":                {askUserSchema("text", "secret"), true},
		"ask mode narrowing":      {NarrowToolSchema("ask_user", askUserSchema("text", "secret"), ProjectOptions{AskMode: true}), false},
		"enum without secret":     {askUserSchema("text"), false},
		"secret field missing":    {withoutField, false},
		"response type no enum":   {unconstrained, false},
		"provider projected form": {ProjectToolArgsSchema("ask_user", askUserSchema("text", "secret"), ProjectOptions{}), true},
	} {
		if got := AskUserAcceptsSecret(tc.schema); got != tc.want {
			t.Errorf("%s: AskUserAcceptsSecret = %t, want %t", name, got, tc.want)
		}
	}
}

func TestNarrowToolSchemaAppliesOnlyTheNamedToolsOption(t *testing.T) {
	raw := askUserSchema("text", "secret")
	if got := NarrowToolSchema("command", raw, ProjectOptions{AskMode: true}); !reflect.DeepEqual(got, raw) {
		t.Fatalf("command narrowing changed an ask_user-shaped schema: %v", got)
	}
	if got := NarrowToolSchema("ask_user", raw, ProjectOptions{}); !reflect.DeepEqual(got, raw) {
		t.Fatalf("ask_user narrowed outside Ask mode: %v", got)
	}
	projected := ProjectToolArgsSchema("ask_user", raw, ProjectOptions{AskMode: true})
	if !reflect.DeepEqual(projected, ProjectArgsSchema(NarrowToolSchema("ask_user", raw, ProjectOptions{AskMode: true}))) {
		t.Fatal("ProjectToolArgsSchema is not the projection of NarrowToolSchema")
	}
}
