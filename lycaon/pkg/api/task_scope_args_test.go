package api_test

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestTaskScopeFromArgs(t *testing.T) {
	t.Parallel()

	scope, err := api.TaskScopeFromArgs(map[string]any{
		"scope": map[string]any{
			"mode":            "write",
			"paths":           []any{"internal/auth/**"},
			"base_overlay_id": "job-a",
		},
	})
	if err != nil {
		t.Fatalf("TaskScopeFromArgs: %v", err)
	}
	if scope.Mode != api.TaskScopeModeWrite || len(scope.Paths) != 1 || scope.BaseOverlayID != "job-a" {
		t.Fatalf("scope = %+v", scope)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"nil args", nil},
		{"no scope key", map[string]any{"agent_type": "implementer"}},
		{"nil scope", map[string]any{"scope": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := api.TaskScopeFromArgs(tc.args)
			if err != nil || got.Mode != api.TaskScopeModeRead {
				t.Fatalf("scope = %+v err = %v", got, err)
			}
		})
	}

	if _, err := api.TaskScopeFromArgs(map[string]any{"scope": "internal/**"}); err == nil {
		t.Fatal("a non-object scope must be rejected")
	}
}
