package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolschema"
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
	for _, key := range FunctionParametersRootForbiddenKeys() {
		err := ValidateFunctionParametersRoot(map[string]any{"type": "object", key: true})
		if err == nil {
			t.Fatalf("%s at root must fail", key)
		}
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("error = %q, want %s", err, key)
		}
	}
}

func TestValidateProviderArgsSchema(t *testing.T) {
	valid := ToolMeta{
		Name:        "read",
		Description: "read file",
		ArgsSchema:  map[string]any{"type": "object"},
	}
	if err := ValidateProviderArgsSchema(valid); err != nil {
		t.Fatalf("valid schema: %v", err)
	}

	cases := []struct {
		name string
		meta ToolMeta
		want string
	}{
		{
			name: "nil schema",
			meta: ToolMeta{Name: "list_dir", Description: "list"},
			want: "ArgsSchema is nil",
		},
		{
			name: "wrong type",
			meta: ToolMeta{Name: "grep", ArgsSchema: map[string]any{"type": "string"}},
			want: `ArgsSchema type "string"`,
		},
		{
			name: "empty name",
			meta: ToolMeta{ArgsSchema: map[string]any{"type": "object"}},
			want: "tool name is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderArgsSchema(tc.meta)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidateRegistryProviderArgsSchemas(t *testing.T) {
	reg := NewDefaultRegistry()
	registerTestDefinition(t, reg, "good", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		return "", nil
	})
	registerTestDefinition(t, reg, "bad", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		return "", nil
	})
	bad, _ := reg.Definition("bad")
	bad.Meta = ToolMeta{Name: "bad", Description: "missing schema"}
	_ = reg.RegisterDefinition(bad)

	err := ValidateRegistryProviderArgsSchemas(reg)
	if err == nil {
		t.Fatal("expected violation")
	}
	if !strings.Contains(err.Error(), `tool "bad"`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestValidateCatalogSchemaParity(t *testing.T) {
	reg := NewDefaultRegistry()
	_ = reg.RegisterDerived(ToolMeta{Name: "read", Description: "clobbered"}, func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		return "", nil
	})

	cfg := &toolschema.Config{
		Tools: map[string]toolschema.Entry{
			"read": {
				Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			},
		},
	}

	err := ValidateCatalogSchemaParity(reg, cfg)
	if err == nil {
		t.Fatal("expected clobber violation")
	}
	if !strings.Contains(err.Error(), "metadata differs from catalog") {
		t.Fatalf("error = %q", err.Error())
	}
}
