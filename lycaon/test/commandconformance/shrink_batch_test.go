package commandconformance

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

type countedReferences struct {
	References
	calls    int
	programs int
}

func (r *countedReferences) Run(ctx context.Context, id referenceuserland.ID, cases []referenceuserland.Case) ([]referenceuserland.Result, error) {
	r.calls++
	r.programs += len(cases)
	return r.References.Run(ctx, id, cases)
}

func TestShrinkBatchesReferenceComparisons(t *testing.T) {
	original := GeneratedProgram{Seed: 7, references: allUserlands, fragments: []generatedFragment{
		{kind: 0, text: "discard", number: 4}, {kind: 8, number: 4}, {kind: 11, text: "discard", number: 3}, {kind: 14, number: 2},
	}}
	refs := &countedReferences{References: &fakeReferences{answer: func(c referenceuserland.Case) referenceuserland.Result {
		if strings.Contains(c.Program, "while") {
			return referenceuserland.Result{Stdout: []byte("reference output")}
		}
		return referenceuserland.Result{}
	}}}
	batches := 0
	result, err := Shrink(t.Context(), original, 128, func(ctx context.Context, cases []Case) ([]bool, error) {
		batches++
		captured, err := Capture(ctx, refs, cases, nil)
		if err != nil {
			return nil, err
		}
		kept := make([]bool, len(cases))
		for i, c := range cases {
			matched, _ := Match(Observation{}, captured[c.ID])
			kept[i] = matched == ""
		}
		return kept, nil
	})
	testutil.FailErr(t, "shrink with batched references", err)
	if !result.Minimal || result.Trials > 128 || batches > 13 {
		t.Fatalf("unbounded reduction %+v batches=%d", result, batches)
	}
	if refs.calls != batches*3 || refs.programs != result.Trials*3 || refs.calls >= refs.programs {
		t.Fatalf("reference setup not amortized calls=%d programs=%d trials=%d batches=%d", refs.calls, refs.programs, result.Trials, batches)
	}
	if len(result.Program.fragments) != 1 || result.Program.fragments[0].kind != 8 || result.Program.fragments[0].number != 1 {
		t.Fatalf("lost original mismatch %+v", result.Program)
	}
}
