package turnload

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
)

func TestReleaseVocabularyKeepsNewToolsOutOfPreloadEncoding(t *testing.T) {
	spec := testSpec()
	spec.Tools.Options = map[string]string{"command": "Run a command", "git_commit": "Commit staged changes"}
	original := fixtureCandidates()
	expanded := append(append([]Candidate(nil), original...), Candidate{Kind: KindTool, ID: "new_tool", Description: "An unrelated extension"})
	if !reflect.DeepEqual(spec.Questions(original), spec.Questions(expanded)) {
		t.Fatal("an unvalidated tool changed the release's encoded questions")
	}
	fake := &decidetest.Fake{Answers: decide.Answers{
		"tools": {Kind: decide.KindMulti, Probabilities: map[string]float64{"command": 0.9, "new_tool": 1}},
	}}
	got := Decide(t.Context(), fake, spec, State{User: "run tests"}, expanded)
	if got.Tools["command"] != 0.9 || len(got.Tools) != 1 {
		t.Fatalf("release predictions = %+v", got.Tools)
	}
	for _, candidate := range got.Candidates {
		if candidate.ID == "new_tool" {
			t.Fatal("receipt listed an unscored tool as a model candidate")
		}
	}
}

func TestReleaseVocabularyPinsDescriptionWithoutWideningSurface(t *testing.T) {
	spec := testSpec()
	spec.Tools.Options = map[string]string{"command": "Run a command", "git_commit": "Commit staged changes"}
	candidates := []Candidate{{Kind: KindTool, ID: "command", Description: "Changed live documentation"}}
	questions := spec.Questions(candidates)
	if !reflect.DeepEqual(questions["tools"].Options, map[string]string{"command": "Run a command"}) {
		t.Fatalf("encoded options = %+v", questions["tools"].Options)
	}
}

func TestIndependentQuestionsEncodeToolsAndGuidesAlikeAndAskNoKind(t *testing.T) {
	spec := testSpec()
	spec.Tools.Independent = true
	spec.Guides.Omittable = []string{"survey-ladder"}
	questions := spec.Questions(fixtureCandidates())
	if len(questions) != 2 || !questions["tools"].Independent || !questions["guides"].Independent || len(questions["tools"].Options) != 2 || len(questions["guides"].Options) != 2 {
		t.Fatalf("independent questions = %+v", questions)
	}
	if _, asked := questions["kind"]; asked {
		t.Fatal("an independent head answers no kind question")
	}
	fake := &decidetest.Fake{Answers: decide.Answers{
		"tools":  {Kind: decide.KindMulti, Probabilities: map[string]float64{"command": 0.9}},
		"guides": {Kind: decide.KindMulti, Probabilities: map[string]float64{"survey-ladder": 0.05, "read-tool": 0.9}, Confidence: 0.95},
	}}
	got := Decide(t.Context(), fake, spec, State{User: "run tests"}, fixtureCandidates())
	if got.Abstained || len(got.Tools) != 1 || len(got.Omitted) != 1 || got.Kind != "" {
		t.Fatalf("independent decision = %+v", got)
	}
}
