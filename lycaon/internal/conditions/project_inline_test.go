package conditions_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRootlessSummarizeRequiresInlineContentOnly(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions", err)
	for _, row := range []struct {
		args  map[string]any
		needs bool
	}{
		{map[string]any{"content": "already observed text"}, false},
		{map[string]any{"content": "text", "path": "."}, true},
		{map[string]any{"paths": []any{"."}}, true},
		{map[string]any{"pattern": ".*"}, true},
		{map[string]any{"cursor": "previous"}, true},
		{map[string]any{}, true},
	} {
		got, err := reg.Evaluate("tool_requires_project_roots", conditions.EvalContext{ToolName: "summarize", ToolArgs: row.args})
		testutil.FailErr(t, "evaluate rootless selection", err)
		if got != row.needs {
			t.Fatalf("args=%v got=%v want=%v", row.args, got, row.needs)
		}
	}
}
