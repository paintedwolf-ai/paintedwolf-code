package contract

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// shippedToolArgsSchema returns the argument schema of a shipped tool or operation.
func shippedToolArgsSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	cfg, err := toolschema.LoadSchemaDirWithOperations(dir)
	contractcheck.FailErr(t, "load tool schemas", err)
	meta, ok := cfg.ToolMeta(name)
	if !ok || len(meta.ArgsSchema) == 0 {
		t.Fatalf("shipped tool %q has no argument schema", name)
	}
	return meta.ArgsSchema
}

type toolArgsCase struct {
	name   string
	args   map[string]any
	accept bool
}

func assertToolArgs(t *testing.T, tool string, cases []toolArgsCase) {
	t.Helper()
	schema := shippedToolArgsSchema(t, tool)
	for _, tc := range cases {
		err := tools.ValidateToolArgs(schema, tc.args)
		if tc.accept {
			if err != nil {
				t.Errorf("%s %s: rejected: %v", tool, tc.name, err)
			}
			continue
		}
		var invalid *jsonschema.ValidationError
		if !errors.As(err, &invalid) {
			t.Errorf("%s %s: err = %v, want a schema validation error", tool, tc.name, err)
		}
	}
}

func TestStructuralIntelligenceSchemaExtensions(t *testing.T) {
	t.Parallel()
	assertToolArgs(t, "grep", []toolArgsCase{
		{name: "structural search", args: map[string]any{"pattern": "foo($A)", "structural": true, "lang": "go"}, accept: true},
	})
	assertToolArgs(t, "code_rewrite", []toolArgsCase{
		{name: "codemod", args: map[string]any{"path": "a.go", "pattern": "foo($A)", "rewrite": "bar($A)", "lang": "go", "dry_run": true}, accept: true},
		{name: "no target", args: map[string]any{"pattern": "foo($A)", "rewrite": "bar($A)"}},
	})
	base := map[string]any{"path": "a.go", "old_string": "a", "new_string": "b"}
	cases := []toolArgsCase{{name: "text edit", args: base, accept: true}}
	for _, key := range []string{"symbol", "kind", "insert_before", "insert_after"} {
		args := map[string]any{key: "x"}
		for k, v := range base {
			args[k] = v
		}
		cases = append(cases, toolArgsCase{name: "with " + key, args: args})
	}
	assertToolArgs(t, "edit", cases)
}

func TestWebSearchSchemaOmitsProvider(t *testing.T) {
	t.Parallel()
	assertToolArgs(t, "web_search", []toolArgsCase{
		{name: "query and limit", args: map[string]any{"query": "q", "limit": 5}, accept: true},
		{name: "provider", args: map[string]any{"query": "q", "provider": "direct"}},
	})
}

// Each checklist row carries one item id, so the schema's item_ids bound is
// the author row budget update_progress enforces.
func TestUpdateProgressToolSchemaMatchesProgressLimits(t *testing.T) {
	t.Parallel()
	ids := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = strconv.Itoa(i)
		}
		return out
	}
	assertToolArgs(t, "update_progress", []toolArgsCase{
		{name: "row budget", args: map[string]any{"content": "- [ ] a", "item_ids": ids(progress.MaxAuthorProgressLines)}, accept: true},
		{name: "over row budget", args: map[string]any{"content": "- [ ] a", "item_ids": ids(progress.MaxAuthorProgressLines + 1)}},
	})
}
