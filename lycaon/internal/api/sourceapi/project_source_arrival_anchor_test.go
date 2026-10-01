package sourceapi

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
)

// fakeAncestry answers reachability from a declared parent map.
type fakeAncestry struct {
	parent map[string]string
}

func (f *fakeAncestry) reaches(ancestor, descendant string) bool {
	for at := descendant; at != ""; at = f.parent[at] {
		if at == ancestor {
			return true
		}
	}
	return false
}

// anchorFor preloads ancestry answers from f.
func anchorFor(t *testing.T, f *fakeAncestry, commits []string, chain []sourceledger.GitTransition) *arrivalAnchor {
	t.Helper()
	a := &arrivalAnchor{chain: chain, memo: map[string]bool{}, budget: arrivalAncestryBudget}
	heads := map[string]struct{}{}
	for _, w := range chain {
		if w.FromCommit != "" {
			heads[w.FromCommit] = struct{}{}
		}
		if w.ToCommit != "" {
			heads[w.ToCommit] = struct{}{}
		}
	}
	for _, c := range commits {
		for head := range heads {
			a.memo[c+":"+head] = f.reaches(c, head)
		}
	}
	return a
}

// Unbounded repository-arrival windows cannot attribute existing commits.
func TestArrivalAnchorIgnoresAWindowWithNoLowerBound(t *testing.T) {
	f := &fakeAncestry{parent: map[string]string{"c": "b", "b": "a", "a": "init"}}
	chain := []sourceledger.GitTransition{
		{ID: "appeared", ToCommit: "a"}, // repo_appeared: no FromCommit
	}
	a := anchorFor(t, f, []string{"init", "a"}, chain)

	for _, commit := range []string{"init", "a"} {
		if got, found := a.arrivalOf(commit); found {
			t.Fatalf("commit %q attributed to unbounded window %q; a window with no "+
				"lower bound cannot show anything arrived in it", commit, got.ID)
		}
	}
}

// Later bounded windows still attribute their new commits.
func TestArrivalAnchorStillAttributesAfterAnUnboundedWindow(t *testing.T) {
	f := &fakeAncestry{parent: map[string]string{"c": "b", "b": "a", "a": "init"}}
	chain := []sourceledger.GitTransition{
		{ID: "appeared", ToCommit: "a"},
		{ID: "landed", FromCommit: "a", ToCommit: "c"},
	}
	a := anchorFor(t, f, []string{"init", "a", "b", "c"}, chain)

	for _, commit := range []string{"init", "a"} {
		if got, found := a.arrivalOf(commit); found {
			t.Fatalf("pre-existing commit %q attributed to %q", commit, got.ID)
		}
	}
	for _, commit := range []string{"b", "c"} {
		got, found := a.arrivalOf(commit)
		if !found {
			t.Fatalf("commit %q arrived in the bounded window but was not attributed", commit)
		}
		if got.ID != "landed" {
			t.Fatalf("commit %q attributed to %q, want landed", commit, got.ID)
		}
	}
}

func TestArrivalAnchorAttributesOnlyWhatTheWindowAdded(t *testing.T) {
	f := &fakeAncestry{parent: map[string]string{"c": "b", "b": "a", "a": "init"}}
	chain := []sourceledger.GitTransition{
		{ID: "landed", FromCommit: "a", ToCommit: "c"},
	}
	a := anchorFor(t, f, []string{"init", "a", "b", "c"}, chain)

	if got, found := a.arrivalOf("init"); found {
		t.Fatalf("init attributed to %q, want no arrival", got.ID)
	}
	if got, found := a.arrivalOf("b"); !found || got.ID != "landed" {
		t.Fatalf("commit b = (%q, %v), want landed", got.ID, found)
	}
}

// Unknown ancestry cannot establish a new arrival.
func TestArrivalAnchorFailsClosedWhenAncestryIsUnknown(t *testing.T) {
	chain := []sourceledger.GitTransition{
		{ID: "landed", FromCommit: "a", ToCommit: "c"},
	}
	// Empty memo and budget force unknown ancestry.
	a := &arrivalAnchor{chain: chain, memo: map[string]bool{}, budget: 0}

	if got, found := a.arrivalOf("init"); found {
		t.Fatalf("attributed %q on an unanswerable ancestry check", got.ID)
	}
}
