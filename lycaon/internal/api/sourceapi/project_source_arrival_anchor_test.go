package sourceapi

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
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
		if got, found, _ := a.arrivalOf(commit); found {
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
		if got, found, _ := a.arrivalOf(commit); found {
			t.Fatalf("pre-existing commit %q attributed to %q", commit, got.ID)
		}
	}
	for _, commit := range []string{"b", "c"} {
		got, found, _ := a.arrivalOf(commit)
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

	if got, found, _ := a.arrivalOf("init"); found {
		t.Fatalf("init attributed to %q, want no arrival", got.ID)
	}
	if got, found, _ := a.arrivalOf("b"); !found || got.ID != "landed" {
		t.Fatalf("commit b = (%q, %v), want landed", got.ID, found)
	}
}

// Unknown ancestry cannot establish a new arrival, and says so.
func TestArrivalAnchorFailsClosedWhenAncestryIsUnknown(t *testing.T) {
	chain := []sourceledger.GitTransition{
		{ID: "landed", FromCommit: "a", ToCommit: "c"},
	}
	// Empty memo and budget force unknown ancestry.
	a := &arrivalAnchor{chain: chain, memo: map[string]bool{}, budget: 0}

	got, found, answered := a.arrivalOf("init")
	if found || answered {
		t.Fatalf("arrivalOf(init) = (%q, found %v, answered %v) on an unanswerable ancestry check",
			got.ID, found, answered)
	}
	if !errors.Is(a.err, errArrivalBudgetExhausted) {
		t.Fatalf("anchor error = %v, want the exhausted budget", a.err)
	}
}

// lineageOf lists commits newest first, the way git reports a page.
func lineageOf(hashes ...string) []git.GitFileCommit {
	out := make([]git.GitFileCommit, 0, len(hashes))
	for _, h := range hashes {
		out = append(out, git.GitFileCommit{Hash: h, Subject: h, Path: "src/app.ts", BlobOID: "blob-" + h})
	}
	return out
}

// A page carries every row when the anchor answers every arrival.
func TestJoinLineageAnswersTheWholePageWhenItCan(t *testing.T) {
	f := &fakeAncestry{parent: map[string]string{"c": "b", "b": "a", "a": "init"}}
	chain := []sourceledger.GitTransition{
		{ID: "landed", FromCommit: "a", ToCommit: "c"},
	}
	a := anchorFor(t, f, []string{"init", "a", "b", "c"}, chain)

	commits, arrivals, answered := joinLineage(lineageOf("c", "b", "a", "init"),
		map[string]string{"blob-a": "version-a"}, a)
	if answered != 4 || len(commits) != 4 {
		t.Fatalf("answered %d rows %d, want the whole page", answered, len(commits))
	}
	if len(arrivals) != 1 || arrivals[0].ID != "landed" {
		t.Fatalf("arrivals = %+v, want the landed window once", arrivals)
	}
	for _, row := range commits[:2] {
		if row.ArrivalGitChangeID == nil || *row.ArrivalGitChangeID != "landed" {
			t.Fatalf("commit %s = %+v, want attributed to landed", row.Commit, row)
		}
	}
	if commits[2].MatchesVersionID == nil || *commits[2].MatchesVersionID != "version-a" {
		t.Fatalf("retained commit a = %+v", commits[2])
	}
	if commits[3].ArrivalGitChangeID != nil || commits[3].MatchesVersionID != nil {
		t.Fatalf("pre-existing init = %+v, want no arrival and no match", commits[3])
	}
}

// The page ends at the first commit whose arrival went unanswered, so no row
// is presented as having arrived in no window.
func TestJoinLineageEndsThePageWhereAnchoringStops(t *testing.T) {
	f := &fakeAncestry{parent: map[string]string{"c": "b", "b": "a", "a": "init"}}
	chain := []sourceledger.GitTransition{
		{ID: "landed", FromCommit: "a", ToCommit: "c"},
	}
	// Only c has answers; the budget is spent before b asks.
	a := anchorFor(t, f, []string{"c"}, chain)
	a.budget = 0

	commits, arrivals, answered := joinLineage(lineageOf("c", "b", "a", "init"), nil, a)
	if answered != 1 || len(commits) != 1 || commits[0].Commit != "c" {
		t.Fatalf("answered %d rows %+v, want only c", answered, commits)
	}
	if len(arrivals) != 1 || arrivals[0].ID != "landed" {
		t.Fatalf("arrivals = %+v, want c's window", arrivals)
	}
}

// Retained rows need no ancestry, so a spent anchor still answers them.
func TestJoinLineageKeepsRetainedRowsWithoutAncestry(t *testing.T) {
	chain := []sourceledger.GitTransition{
		{ID: "landed", FromCommit: "a", ToCommit: "c"},
	}
	a := &arrivalAnchor{chain: chain, memo: map[string]bool{}, budget: 0}

	commits, _, answered := joinLineage(lineageOf("c", "b"),
		map[string]string{"blob-c": "version-c"}, a)
	if answered != 1 || len(commits) != 1 || commits[0].MatchesVersionID == nil {
		t.Fatalf("answered %d rows %+v, want the retained c alone", answered, commits)
	}
}

func TestAnchorFailureStateNamesTheDeadline(t *testing.T) {
	if got := anchorFailureState(context.DeadlineExceeded); got != wire.SourceGitHistoryStateTimedOut {
		t.Fatalf("deadline state = %q, want timed_out", got)
	}
	if got := anchorFailureState(errArrivalBudgetExhausted); got != wire.SourceGitHistoryStateFailed {
		t.Fatalf("budget state = %q, want failed", got)
	}
	if got := anchorFailureState(nil); got != wire.SourceGitHistoryStateFailed {
		t.Fatalf("nil state = %q, want failed", got)
	}
}
