package git

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveRevisionReadsCommitsBranchesAndRanges(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	commit := func(name, message string) string {
		t.Helper()
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(message+"\n"), 0o644))
		run("add", "-A")
		run("commit", "-m", message)
		return run("rev-parse", "HEAD")
	}
	root := commit("a.txt", "root commit")
	base := commit("b.txt", "shared base")
	trunk := run("symbolic-ref", "--short", "HEAD")
	run("checkout", "-b", "topic")
	topic := commit("topic.txt", "topic work\n\nbody text")
	run("checkout", trunk)
	head := commit("main.txt", "trunk work")

	mgr := NewManager()
	cases := []struct {
		spec                 string
		kind                 RevisionKind
		label, before, after string
		subject              string
	}{
		{spec: topic[:9], kind: RevisionCommit, label: topic[:7] + " topic work", before: base, after: topic, subject: "topic work"},
		{spec: root, kind: RevisionCommit, label: root[:7] + " root commit", before: "", after: root, subject: "root commit"},
		{spec: "HEAD~1", kind: RevisionCommit, label: base[:7] + " shared base", before: root, after: base, subject: "shared base"},
		{spec: trunk, kind: RevisionBranch, label: trunk + " at " + head[:7], before: base, after: head, subject: "trunk work"},
		{spec: "topic", kind: RevisionBranch, label: "topic...HEAD", before: base, after: head},
		{spec: "topic.." + trunk, kind: RevisionRange, label: "topic.." + trunk, before: topic, after: head},
		{spec: "topic...", kind: RevisionRange, label: "topic...HEAD", before: base, after: head},
		{spec: trunk + "...topic", kind: RevisionRange, label: trunk + "...topic", before: base, after: topic},
		{spec: "..topic", kind: RevisionRange, label: "HEAD..topic", before: head, after: topic},
	}
	for _, tc := range cases {
		got, found, err := mgr.ResolveRevision(t.Context(), dir, tc.spec)
		testutil.FailErr(t, "resolve "+tc.spec, err)
		want := RevisionComparison{Spec: tc.spec, Kind: tc.kind, Label: tc.label, Before: tc.before, After: tc.after, Subject: tc.subject}
		if !found || got != want {
			t.Fatalf("%s: found=%v got %+v, want %+v", tc.spec, found, got, want)
		}
	}

	for _, spec := range []string{"no-such-branch", "0000000", "topic..missing", "HEAD:a.txt", "refs/heads/topic~9"} {
		if got, found, err := mgr.ResolveRevision(t.Context(), dir, spec); err != nil || found {
			t.Fatalf("%s resolved: found=%v got %+v err %v", spec, found, got, err)
		}
	}
	for _, spec := range []string{"", "-p", "--output=/tmp/x", "topic..-p", "--", "main topic", "a\tb", "a\x00b", "..", "a..b..c"} {
		if _, _, err := mgr.ResolveRevision(t.Context(), dir, spec); !errors.Is(err, ErrRevisionSpecRejected) {
			t.Fatalf("%q was not rejected: %v", spec, err)
		}
	}
}

func TestResolveRevisionDetachedHeadAndUnrelatedHistory(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	testutil.FailErr(t, "write first", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "first")
	first := run("rev-parse", "HEAD")
	trunk := run("symbolic-ref", "--short", "HEAD")
	run("checkout", "--orphan", "island")
	testutil.FailErr(t, "write island", os.WriteFile(filepath.Join(dir, "island.txt"), []byte("island\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "island")
	run("checkout", "--detach", first)
	mgr := NewManager()
	// With HEAD detached, no branch is the checked-out one.
	got, found, err := mgr.ResolveRevision(t.Context(), dir, trunk)
	testutil.FailErr(t, "resolve trunk", err)
	if !found || got.Kind != RevisionBranch || got.Before != first || got.After != first || got.Label != trunk+"...HEAD" {
		t.Fatalf("detached branch = %+v found=%v", got, found)
	}
	if got, found, err := mgr.ResolveRevision(t.Context(), dir, "island"); err != nil || found {
		t.Fatalf("unrelated branch resolved: %+v found=%v err=%v", got, found, err)
	}
	if got, found, err := mgr.ResolveRevision(t.Context(), dir, "island..."+trunk); err != nil || found {
		t.Fatalf("unrelated range resolved: %+v found=%v err=%v", got, found, err)
	}
}
