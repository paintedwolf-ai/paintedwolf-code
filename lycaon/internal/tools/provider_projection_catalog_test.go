package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestProviderProjectionCatalogRequiredFieldsAreLocal(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load catalog", err)
	for name := range cfg.Tools {
		t.Run(name, func(t *testing.T) {
			meta, ok := cfg.ToolMeta(name)
			if !ok {
				t.Fatal("catalog metadata missing")
			}
			projected := ProjectProviderToolMeta(ToolMeta{Name: name, ArgsSchema: meta.ArgsSchema})
			testutil.FailErr(t, "provider root", ValidateFunctionParametersRoot(projected.ArgsSchema))
			assertLocalRequired(t, projected.ArgsSchema)
		})
	}
}

func assertLocalRequired(t *testing.T, value any) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		props, _ := node["properties"].(map[string]any)
		required, _ := copySchemaList(node["required"])
		for _, raw := range required {
			name, _ := raw.(string)
			if _, ok := props[name]; !ok {
				t.Fatalf("required %q has no local property: %v", name, node)
			}
		}
		for _, child := range node {
			assertLocalRequired(t, child)
		}
	case []any:
		for _, child := range node {
			assertLocalRequired(t, child)
		}
	}
}

func TestProviderProjectionLeavesHostRelationsAuthoritative(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load catalog", err)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"recall", map[string]any{"history": map[string]any{}}},
		{"submit_verdict", map[string]any{"verdict": map[string]any{"verdict": "CLAIMED"}, "cited_evidence": []any{map[string]any{}}}},
		{"ask_user", map[string]any{"prompt": "Credential", "response_type": "secret", "secret": map[string]any{"name": "token", "scope": "project"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta, ok := cfg.ToolMeta(tc.name)
			if !ok {
				t.Fatal("metadata missing")
			}
			projected := ProjectProviderToolMeta(ToolMeta{Name: tc.name, ArgsSchema: meta.ArgsSchema})
			testutil.FailErr(t, "generation guidance", ValidateToolArgs(projected.ArgsSchema, tc.args))
			if err := ValidateToolArgs(meta.ArgsSchema, tc.args); err == nil {
				t.Fatal("host accepted missing relational field")
			}
		})
	}
}
