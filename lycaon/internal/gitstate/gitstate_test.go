package gitstate

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func repoAt(commit, ref string) State {
	return State{Repo: RepoPresent, HeadCommit: commit, HeadRef: ref}
}

func TestClassifyUnmovedPositionIsSilent(t *testing.T) {
	if got := Classify(repoAt("aaa", "main"), repoAt("aaa", "main"), nil); len(got) != 0 {
		t.Fatalf("transitions = %+v", got)
	}
}

func TestClassifyRepoLifecycle(t *testing.T) {
	appeared := Classify(State{Repo: RepoAbsent}, repoAt("aaa", "main"), nil)
	if len(appeared) != 1 || appeared[0].Kind != api.SourceGitChangeRepoAppeared ||
		appeared[0].ToCommit != "aaa" || appeared[0].ToRef != "main" {
		t.Fatalf("appeared = %+v", appeared)
	}
	gone := Classify(repoAt("aaa", "main"), State{Repo: RepoAbsent}, nil)
	if len(gone) != 1 || gone[0].Kind != api.SourceGitChangeRepoGone || gone[0].FromCommit != "aaa" {
		t.Fatalf("gone = %+v", gone)
	}
	if got := Classify(State{Repo: RepoAbsent}, State{Repo: RepoUnreadable}, nil); len(got) != 0 {
		t.Fatalf("absent-to-unreadable narrated: %+v", got)
	}
}

func TestClassifySingleReflogActions(t *testing.T) {
	cases := []struct {
		subject string
		kind    api.SourceGitChangeKind
		detail  string
	}{
		{"commit: Fix the bug", api.SourceGitChangeCommit, "Fix the bug"},
		{"commit (initial): First", api.SourceGitChangeCommit, "First"},
		{"commit (amend): Fix harder", api.SourceGitChangeAmend, "Fix harder"},
		{"checkout: moving from main to feature-x", api.SourceGitChangeCheckout, "moving from main to feature-x"},
		{"merge feature-x: Fast-forward", api.SourceGitChangeMerge, "Fast-forward"},
		{"rebase (finish): returning to refs/heads/main", api.SourceGitChangeRebase, "returning to refs/heads/main"},
		{"rebase -i (pick): Fix the bug", api.SourceGitChangeRebase, "Fix the bug"},
		{"pull: Fast-forward", api.SourceGitChangePull, "Fast-forward"},
		{"reset: moving to HEAD~1", api.SourceGitChangeReset, "moving to HEAD~1"},
		{"cherry-pick: Fix elsewhere", api.SourceGitChangeCherryPick, "Fix elsewhere"},
		{"revert: Revert \"Fix\"", api.SourceGitChangeRevert, "Revert \"Fix\""},
		{"clone: from https://example.com/repo.git", api.SourceGitChangeClone, "from https://example.com/repo.git"},
	}
	for _, tc := range cases {
		got := Classify(repoAt("aaa", "main"), repoAt("bbb", "main"), []RefLogEntry{
			{Commit: "bbb", Subject: tc.subject},
			{Commit: "aaa", Subject: "commit: earlier"},
		})
		if len(got) != 1 {
			t.Fatalf("%q: transitions = %+v", tc.subject, got)
		}
		if got[0].Kind != tc.kind || got[0].Detail != tc.detail {
			t.Fatalf("%q: kind=%s detail=%q", tc.subject, got[0].Kind, got[0].Detail)
		}
		if got[0].FromCommit != "aaa" || got[0].ToCommit != "bbb" ||
			got[0].FromRef != "main" || got[0].ToRef != "main" {
			t.Fatalf("%q: endpoints = %+v", tc.subject, got[0])
		}
	}
}

func TestClassifyExpandsAMultiEntryRun(t *testing.T) {
	got := Classify(repoAt("aaa", "main"), repoAt("ccc", "work"), []RefLogEntry{
		{Commit: "ccc", Subject: "commit: Second"},
		{Commit: "bbb", Subject: "checkout: moving from main to work"},
		{Commit: "aaa", Subject: "commit: earlier"},
	})
	if len(got) != 2 {
		t.Fatalf("transitions = %+v", got)
	}
	// Oldest first: the checkout, then the commit.
	if got[0].Kind != api.SourceGitChangeCheckout || got[0].FromCommit != "aaa" ||
		got[0].ToCommit != "bbb" || got[0].FromRef != "main" || got[0].ToRef != "" {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].Kind != api.SourceGitChangeCommit || got[1].FromCommit != "bbb" ||
		got[1].ToCommit != "ccc" || got[1].ToRef != "work" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestClassifyUnexplainedMovementIsUnknown(t *testing.T) {
	// prev's commit never appears: the reflog expired or belongs to a fresh clone.
	got := Classify(repoAt("aaa", "main"), repoAt("bbb", "main"), []RefLogEntry{
		{Commit: "bbb", Subject: "commit: unrelated"},
		{Commit: "zzz", Subject: "commit: unrelated"},
	})
	if len(got) != 1 || got[0].Kind != api.SourceGitChangeUnknown {
		t.Fatalf("transitions = %+v", got)
	}
	if got[0].FromCommit != "aaa" || got[0].ToCommit != "bbb" {
		t.Fatalf("endpoints = %+v", got[0])
	}
}

func TestClassifyRefOnlyChange(t *testing.T) {
	confirmed := Classify(repoAt("aaa", "main"), repoAt("aaa", "work"), []RefLogEntry{
		{Commit: "aaa", Subject: "checkout: moving from main to work"},
	})
	if len(confirmed) != 1 || confirmed[0].Kind != api.SourceGitChangeCheckout {
		t.Fatalf("confirmed = %+v", confirmed)
	}
	// A rename (branch -m) writes no HEAD reflog entry; the movement is stated
	// as unexplained, never guessed to be a checkout.
	unexplained := Classify(repoAt("aaa", "main"), repoAt("aaa", "renamed"), []RefLogEntry{
		{Commit: "aaa", Subject: "commit: earlier"},
	})
	if len(unexplained) != 1 || unexplained[0].Kind != api.SourceGitChangeUnknown {
		t.Fatalf("unexplained = %+v", unexplained)
	}
}

func TestClassifyCollapsesAnOverlongRun(t *testing.T) {
	reflog := make([]RefLogEntry, 0, MaxTransitionsPerObservation+3)
	for i := MaxTransitionsPerObservation + 1; i >= 1; i-- {
		reflog = append(reflog, RefLogEntry{
			Commit: fmt.Sprintf("c%02d", i), Subject: "commit: step",
		})
	}
	reflog = append(reflog, RefLogEntry{Commit: "aaa", Subject: "commit: base"})
	got := Classify(repoAt("aaa", "main"), repoAt(reflog[0].Commit, "main"), reflog)
	if len(got) != 1 || got[0].Kind != api.SourceGitChangeOther {
		t.Fatalf("transitions = %+v", got)
	}
	if got[0].FromCommit != "aaa" || got[0].ToCommit != reflog[0].Commit || got[0].Detail == "" {
		t.Fatalf("collapsed = %+v", got[0])
	}
}

func TestClassifyUnrecognizedActionIsOtherWithDetail(t *testing.T) {
	got := Classify(repoAt("aaa", "main"), repoAt("bbb", "main"), []RefLogEntry{
		{Commit: "bbb", Subject: "filter-branch: rewritten"},
		{Commit: "aaa", Subject: "commit: earlier"},
	})
	if len(got) != 1 || got[0].Kind != api.SourceGitChangeOther {
		t.Fatalf("transitions = %+v", got)
	}
	if got[0].Detail != "filter-branch: rewritten" {
		t.Fatalf("detail = %q", got[0].Detail)
	}
}
