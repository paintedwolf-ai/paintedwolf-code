package conformance_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

func TestGeneratedShrinkerThroughCommandEntry(t *testing.T) {
	programs, err := commandconformance.Generate(0, 1)
	testutil.FailErr(t, "generate command entry trial", err)
	original := programs[0]
	// Select the quotation fragment so the injected reference defect has one cause.
	for {
		found := false
		for _, candidate := range original.ReductionCandidates() {
			if strings.HasPrefix(candidate.Case().Program, "printf '%s|%s\\n'") {
				original, found = candidate, true
				break
			}
		}
		if !found {
			break
		}
	}
	runner := newConformanceRunner(t, gate.PostureLight, true)
	refs := map[referenceuserland.ID]commandconformance.Snapshot{referenceuserland.GNU: {Status: 0}}
	first := runner.runCase(t, original.Case())
	defect := generatedMismatch(first, refs)
	if defect.Kind != "observation" || defect.References[referenceuserland.GNU].Kind != "stdout" {
		t.Fatalf("injected reference defect %+v outcome %+v", defect, first.Outcome)
	}
	retains := func(_ context.Context, cases []commandconformance.Case) ([]bool, error) {
		kept := make([]bool, len(cases))
		for i, c := range cases {
			kept[i] = reflect.DeepEqual(generatedMismatch(runner.runCase(t, c), refs), defect)
		}
		return kept, nil
	}
	result, err := commandconformance.Shrink(t.Context(), original, 32, retains)
	testutil.FailErr(t, "reduce through command entry", err)
	if !result.Minimal {
		t.Fatalf("did not finish minimization %+v", result)
	}
	for _, candidate := range result.Program.ReductionCandidates() {
		kept, err := retains(t.Context(), []commandconformance.Case{candidate.Case()})
		testutil.FailErr(t, "check command entry fixed point", err)
		if kept[0] {
			t.Fatal("command entry reproducer still reduces")
		}
	}
}
