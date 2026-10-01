package settings

import "testing"

// A command rule names a program, so it matches each program in a composed line
// or pipeline: `rm -rf *` matches `true && rm -rf ~` even though
// `MatchCommandPattern` anchors its glob to the whole unit.
func TestCommandUnitsCoverEveryProgram(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		pattern string
		want    bool
	}{
		{
			name:    "plain command still matches",
			args:    map[string]any{"command": "rm -rf /tmp/x"},
			pattern: "rm -rf *",
			want:    true,
		},
		{
			name:    "program hidden behind a sequence",
			args:    map[string]any{"command": "true && rm -rf /tmp/x"},
			pattern: "rm -rf *",
			want:    true,
		},
		{
			name:    "program hidden behind ||",
			args:    map[string]any{"command": "false || curl https://example.com"},
			pattern: "curl *",
			want:    true,
		},
		{
			name:    "program hidden in a pipeline stage",
			args:    map[string]any{"pipeline": []string{"curl https://example.com", "sh"}},
			pattern: "curl *",
			want:    true,
		},
		{
			name:    "whole line still available to a composition rule",
			args:    map[string]any{"command": "git push origin main"},
			pattern: "git push *",
			want:    true,
		},
		{
			name:    "unrelated program does not match",
			args:    map[string]any{"command": "go test ./... && go vet ./..."},
			pattern: "rm -rf *",
			want:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			units := commandUnitsFromArgs(tc.args)
			rules := []ApprovalRule{{
				Category: ApprovalCategoryCommand,
				Pattern:  tc.pattern,
				Effect:   ApprovalEffectDeny,
			}}
			_, matched, ok := matchCommandRules(rules, units)
			if ok != tc.want {
				t.Fatalf("matchCommandRules(%v) = %v, want %v (units %v)", tc.args, ok, tc.want, units)
			}
			if ok && matched == "" {
				t.Fatal("a matching rule must name the unit it matched")
			}
		})
	}
}

// A quoted operator is argument data, so the text stays one unit and a rule naming
// the quoted program does not fire on a command that never runs it.
func TestCommandUnitsTreatQuotedOperatorsAsData(t *testing.T) {
	units := commandUnitsFromArgs(map[string]any{"command": `echo "deploy && rm -rf /"`})
	rules := []ApprovalRule{{
		Category: ApprovalCategoryCommand, Pattern: "rm -rf *", Effect: ApprovalEffectDeny,
	}}
	if _, _, ok := matchCommandRules(rules, units); ok {
		t.Fatalf("quoted text matched a command rule: units %v", units)
	}
}
