package decidetest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadAllAnswersEveryMultiOptionYes(t *testing.T) {
	res, err := LoadAll{}.Decide(t.Context(), decide.HeadTurnLoad, nil, map[string]decide.Question{
		"tools": decide.Multi("Which tools?", map[string]string{"task": "Delegate", "copy": "Copy a file"}),
		"ready": decide.Noul("Ready?"),
		"kind":  decide.Choice("Which kind?", map[string]string{"answer_only": "Answer"}),
	})
	testutil.FailErr(t, "LoadAll.Decide", err)
	tools := res.Answers["tools"]
	if tools.Kind != decide.KindMulti || tools.Probabilities["task"] != 1 || tools.Probabilities["copy"] != 1 || tools.Confidence != 1 {
		t.Fatalf("multi answer = %+v, want every option P=1", tools)
	}
	if ready := res.Answers["ready"]; ready.Noul != 1 || ready.Confidence != 1 {
		t.Fatalf("yes/no answer = %+v, want a confident yes", ready)
	}
	if kind := res.Answers["kind"]; kind.Choice != "" || kind.Confidence != 0 {
		t.Fatalf("choice answer = %+v, want an abstention", kind)
	}
}
