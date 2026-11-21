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

func TestCommandLanguageReachableMixedModeAndMutatingForms(t *testing.T) {
	readOnly := map[string]bool{"command": true}
	mutating := map[string]bool{"command": true, ReachCommandMutation: true}
	for _, form := range []string{"find", "sed", "yq"} {
		if !CommandLanguageReachable(form, readOnly) {
			t.Fatalf("%s has a read-only mode and must be reachable on read-only command surfaces", form)
		}
	}
	for _, form := range []string{"edit", "replace-lines", "jq-edit", "rm", "mkdir"} {
		if CommandLanguageReachable(form, readOnly) {
			t.Fatalf("%s requires mutation and must not be reachable on read-only command surfaces", form)
		}
		if !CommandLanguageReachable(form, mutating) {
			t.Fatalf("%s must be reachable when command mutation is held", form)
		}
	}

	// Concrete mixed-mode invocations distinguish read-only examples from mutating flags.
	readExamples := "Chain `find . -maxdepth 2 -type d`, `sed -n '1,20p' a.go`, `yq '.a.b' cfg.yaml`, `sort file.txt`, and `tar -tf archive.tar`."
	if got := MutatingMixedModeInvocationsIn(readExamples); len(got) != 0 {
		t.Fatalf("read-only mixed-mode examples flagged as mutating: %v", got)
	}
	mutatingExamples := "Avoid `sed -i 's/a/b/' a.go`, `find . -name '*.tmp' -delete`, `yq -i '.a = 1' cfg.yaml`, `sort -o out.txt in.txt`, and `tar -czf out.tgz .` on read-only surfaces."
	wantMutating := []string{"find", "sed", "sort", "tar", "yq"}
	gotMutating := MutatingMixedModeInvocationsIn(mutatingExamples)
	if len(gotMutating) != len(wantMutating) {
		t.Fatalf("MutatingMixedModeInvocationsIn = %v, want %v", gotMutating, wantMutating)
	}
	for i := range wantMutating {
		if gotMutating[i] != wantMutating[i] {
			t.Fatalf("MutatingMixedModeInvocationsIn[%d] = %q, want %q", i, gotMutating[i], wantMutating[i])
		}
	}
}
