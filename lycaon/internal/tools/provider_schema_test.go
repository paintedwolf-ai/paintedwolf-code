package tools

import (
	"context"
	"strings"
	"testing"
)

func TestValidateFunctionParametersRoot(t *testing.T) {
	if err := ValidateFunctionParametersRoot(map[string]any{"type": "object"}); err != nil {
		t.Fatalf("plain object: %v", err)
	}
	if err := ValidateFunctionParametersRoot(nil); err == nil {
		t.Fatal("nil schema must fail")
	}
	if err := ValidateFunctionParametersRoot(map[string]any{"type": "string"}); err == nil {
		t.Fatal("non-object type must fail")
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"} {
		err := ValidateFunctionParametersRoot(map[string]any{"type": "object", key: true})
		if err == nil {
			t.Fatalf("%s at root must fail", key)
		}
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("error = %q, want %s", err, key)
		}
	}
}

func TestRegistryRejectsIncompleteProviderMetadata(t *testing.T) {
	reg := NewDefaultRegistry()
	registerTestDefinition(t, reg, "good", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		return "", nil
	})
	registerTestDefinition(t, reg, "bad", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		return "", nil
	})
	bad, _ := reg.Definition("bad")
	bad.Meta = ToolMeta{Name: "bad", Description: "missing schema"}
	if err := reg.RegisterDefinition(bad); err == nil {
		t.Fatal("missing schema was published")
	}
}
