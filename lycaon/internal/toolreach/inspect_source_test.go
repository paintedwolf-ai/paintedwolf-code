package toolreach

import (
	"slices"
	"testing"
)

func TestInspectSourceFormsKeepArgumentsInOneFixedOperation(t *testing.T) {
	for _, fixture := range []struct {
		operation string
		args      map[string]any
		want      []string
	}{
		{operation: "read", args: map[string]any{"path": "a; alter"}, want: []string{"cat", "-n", "--", "a; alter"}},
		{operation: "list", args: map[string]any{"path": "-secret"}, want: []string{"ls", "--", "-secret"}},
		{operation: "search", args: map[string]any{"pattern": "-flag"}, want: []string{"rg", "-n", "-e", "-flag", "--", "."}},
		{operation: "query_json", args: map[string]any{"path": "data.json", "query": ".items[]"}, want: []string{"jq", "--", ".items[]", "data.json"}},
		{operation: "query_yaml", args: map[string]any{"path": "data.yaml", "query": ".items[]"}, want: []string{"jq", "--yaml-input", "--", ".items[]", "data.yaml"}},
		{operation: "git_head", args: map[string]any{"path": "."}},
		{operation: "external", args: map[string]any{"path": "unavailable"}},
		{operation: "read", args: map[string]any{}},
	} {
		fixture.args["operation"] = fixture.operation
		if got := InspectSourceArgv(fixture.args); !slices.Equal(got, fixture.want) {
			t.Errorf("%s argv = %q, want %q", fixture.operation, got, fixture.want)
		}
	}
}
