package toolstest

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestValidateProviderArgsSchema(t *testing.T) {
	valid := tools.ToolMeta{
		Name:        "read",
		Description: "read file",
		ArgsSchema:  map[string]any{"type": "object"},
	}
	if err := ValidateProviderArgsSchema(valid); err != nil {
		t.Fatalf("valid schema: %v", err)
	}

	cases := []struct {
		name string
		meta tools.ToolMeta
		want string
	}{
		{
			name: "nil schema",
			meta: tools.ToolMeta{Name: "list_dir", Description: "list"},
			want: "ArgsSchema is nil",
		},
		{
			name: "wrong type",
			meta: tools.ToolMeta{Name: "grep", ArgsSchema: map[string]any{"type": "string"}},
			want: `ArgsSchema type "string"`,
		},
		{
			name: "empty name",
			meta: tools.ToolMeta{ArgsSchema: map[string]any{"type": "object"}},
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

func TestValidateCatalogSchemaParity(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	_ = reg.RegisterDerived(tools.ToolMeta{Name: "read", Description: "clobbered", ArgsSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
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
