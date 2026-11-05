package native

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

// Host quoting must keep expansions and apostrophes from reaching the parser as syntax.
func TestQuoteProgramKeepsValuesLiteral(t *testing.T) {
	got := quoteProgram([]string{"code-rewrite", "--pattern", "f($A, 'x')"})
	want := `'code-rewrite' '--pattern' 'f($A, '\''x'\'')'`
	if got != want {
		t.Fatalf("program = %s", got)
	}
}
