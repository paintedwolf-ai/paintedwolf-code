package toolcontract

import "testing"

func TestHyphenatedCommandFormsInAdmitsDeclaredAliases(t *testing.T) {
	corpus := "Run `repo-map` or its `survey-repo` spelling; `not-a-form` is undeclared."
	got := HyphenatedCommandFormsIn(corpus)
	want := []string{"repo-map", "survey-repo"}
	if len(got) != len(want) {
		t.Fatalf("HyphenatedCommandFormsIn(%q) = %v, want %v", corpus, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HyphenatedCommandFormsIn(%q) = %v, want %v", corpus, got, want)
		}
	}
}

func TestCommandLanguageReachableResolvesAliasGate(t *testing.T) {
	held := map[string]bool{"command": true, "survey_repo": true}
	if !CommandLanguageReachable("survey-repo", held) {
		t.Fatal("survey-repo should be reachable with command and survey_repo held")
	}
	if CommandLanguageReachable("survey-repo", map[string]bool{"command": true}) {
		t.Fatal("survey-repo must require the survey_repo tool")
	}
	if CommandLanguageReachable("survey-repo", map[string]bool{"survey_repo": true}) {
		t.Fatal("survey-repo must require command")
	}
	if CommandLanguageReachable("repo-map", held) != CommandLanguageReachable("survey-repo", held) {
		t.Fatal("alias and canonical form must agree")
	}
}

func TestCommandFormToolOfResolvesAlias(t *testing.T) {
	if got := CommandFormToolOf("survey-repo"); got != "survey_repo" {
		t.Fatalf("CommandFormToolOf(survey-repo) = %q, want survey_repo", got)
	}
	if got := CommandFormToolOf("repo-map"); got != "survey_repo" {
		t.Fatalf("CommandFormToolOf(repo-map) = %q, want survey_repo", got)
	}
	if got := CommandFormToolOf("not-a-form"); got != "" {
		t.Fatalf("CommandFormToolOf(not-a-form) = %q, want empty", got)
	}
}
