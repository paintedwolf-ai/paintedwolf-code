package summarize

import (
	"context"
	"testing"
)

type countedOutline struct{ calls int }

func (o *countedOutline) Outline(_ context.Context, path string) (StructureCandidate, bool) {
	o.calls++
	return StructureCandidate{RelPath: path, Head: "observed source"}, true
}

func TestCuratorOutlineBudgetsSpanDirectoriesAndRepeatedFills(t *testing.T) {
	provider := &countedOutline{}
	o := &curatorOutliner{provider: provider, fileLimit: 3, nameLimit: 2, observations: map[string]outlineObservation{}}
	for _, p := range []string{"a/first.go", "b/second.go"} {
		if _, ok := o.indexOutline(t.Context(), p); !ok {
			t.Fatalf("missing admitted outline %s", p)
		}
	}
	if _, ok := o.indexOutline(t.Context(), "c/third.go"); ok {
		t.Fatal("name indexing exceeded the invocation allowance")
	}
	if _, ok := o.Outline(t.Context(), "c/third.go"); !ok {
		t.Fatal("detail could not use remaining file allowance")
	}
	if _, ok := o.Outline(t.Context(), "d/fourth.go"); ok {
		t.Fatal("detail exceeded the invocation file allowance")
	}
	if _, ok := o.indexOutline(t.Context(), "a/first.go"); !ok || provider.calls != 3 {
		t.Fatalf("repeated fill lost or reread observation: calls=%d", provider.calls)
	}
}
