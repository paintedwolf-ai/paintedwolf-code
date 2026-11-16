package toolreach

import (
	"reflect"
	"testing"
)

func TestFormArgvMapsEveryShippedArgument(t *testing.T) {
	cases := []struct {
		name string
		argv func(map[string]any) []string
		args map[string]any
		want []string
	}{
		{"summarize paths", SummarizeArgv,
			map[string]any{"paths": []any{"a", "b"}, "task": "why", "pattern": "x+", "cursor": "c"},
			[]string{"summarize", "--path", "a", "--path", "b", "--focus", "why", "--pattern", "x+", "--cursor", "c"}},
		{"summarize content", SummarizeArgv, map[string]any{"content": "text"}, []string{"summarize", "--content", "text"}},
		{"survey_repo", SurveyRepoArgv, map[string]any{"bundle": "api_routes", "path": "src"},
			[]string{"repo-map", "--path", "src", "--bundle", "api_routes"}},
		{"code_rewrite", CodeRewriteArgv,
			map[string]any{"pattern": "f($A)", "rewrite": "g($A)", "paths": []any{"a"}, "recursive": true, "dry_run": true, "lang": "go", "syntax_override_reason": "r"},
			[]string{"code-rewrite", "--pattern", "f($A)", "--rewrite", "g($A)", "--path", "a", "--recursive", "--dry-run", "--lang", "go", "--syntax-override-reason", "r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.argv(tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %q", got)
			}
		})
	}
}

func TestFormCommandQuotesTheFormAndRefusesOtherTools(t *testing.T) {
	got, ok := FormCommand("survey_repo", map[string]any{"path": "my dir"})
	if !ok || got != "repo-map --path 'my dir'" {
		t.Fatalf("form command = %q, %v", got, ok)
	}
	if _, ok := FormCommand("command", map[string]any{"command": "ls"}); ok {
		t.Fatal("a tool without a form produced one")
	}
}
