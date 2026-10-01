package survey

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
)

func initNestedGitDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
}

func initNestedGitWorktreeFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/main/.git/worktrees/foo\n"), 0o644); err != nil {
		testutil.FailErr(t, "write .git file", err)
	}
}

func subtreeContains(root *summarize.SubtreeNode, path string) bool {
	if root == nil {
		return false
	}
	if root.Path == path {
		return true
	}
	if root.LoadChildren != nil {
		root.LoadChildren(context.Background(), root)
	}
	for _, child := range root.Children {
		if subtreeContains(child, path) {
			return true
		}
	}
	return false
}

func TestNestedRepoWorktreeFilePruned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package main\n")
	writeFile(t, dir, "nested/big.go", "package nested\n")
	initNestedGitWorktreeFile(t, filepath.Join(dir, "nested"))

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "."})
	testutil.FailErr(t, "gather", err)
	if subtreeContains(res.Subtree, "nested/big.go") {
		t.Fatal("worktree nested repo remained in subtree")
	}
	if !subtreeContains(res.Subtree, "a.go") {
		t.Fatal("root source absent from subtree")
	}
}

func TestTargetRootRepoNotPruned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package main\n")
	initNestedGitDir(t, dir)

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "."})
	testutil.FailErr(t, "gather", err)
	if !subtreeContains(res.Subtree, "a.go") {
		t.Fatal("target root source absent from subtree")
	}
}

func TestNestedRepoPrunedFromGrep(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package main\nvar secretToken = 1\n")
	writeFile(t, dir, "nested/big.go", "package nested\nvar secretToken = 2\n")
	initNestedGitDir(t, filepath.Join(dir, "nested"))

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Pattern: "secretToken"})
	testutil.FailErr(t, "pattern gather", err)
	for _, s := range res.SampleMatches {
		if strings.HasPrefix(s.Path, "nested/") {
			t.Fatalf("pattern grep should not return nested repo paths, got %+v", res.SampleMatches)
		}
	}
	if len(res.SampleMatches) == 0 || res.SampleMatches[0].Path != "a.go" {
		t.Fatalf("expected match in a.go only, got %+v", res.SampleMatches)
	}
}

func TestPruneNestedVCSCapDisables(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "nested/big.go", "package nested\n")
	initNestedGitDir(t, filepath.Join(dir, "nested"))

	caps := summarize.DefaultCaps()
	caps.Gather.PruneNestedVCS = false
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "."})
	testutil.FailErr(t, "gather", err)
	if !subtreeContains(res.Subtree, "nested/big.go") {
		t.Fatal("disabled pruning omitted nested source")
	}
	if res.Stats.NestedReposPruned != 0 {
		t.Fatalf("metric = %d, want 0 when cap disabled", res.Stats.NestedReposPruned)
	}
}

func TestNestedReposPrunedMetric(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package main\n")
	writeFile(t, dir, "nested/big.go", "package nested\n")
	initNestedGitDir(t, filepath.Join(dir, "nested"))

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "."})
	testutil.FailErr(t, "gather", err)
	if res.Stats.NestedReposPruned != 1 {
		t.Fatalf("nested_repos_pruned = %d, want 1", res.Stats.NestedReposPruned)
	}
}

func TestNestedRepoPrunedFromSubtree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package main\n")
	writeFile(t, dir, "nested/big.go", "package nested\n")
	initNestedGitDir(t, filepath.Join(dir, "nested"))

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "."})
	testutil.FailErr(t, "gather", err)
	if res.Subtree == nil {
		t.Fatal("expected subtree")
	}
	if !subtreeContains(res.Subtree, "a.go") {
		t.Fatal("root source absent from subtree")
	}
	var walk func(*summarize.SubtreeNode)
	walk = func(n *summarize.SubtreeNode) {
		if n == nil {
			return
		}
		if strings.HasPrefix(n.Path, "nested/") {
			t.Fatalf("nested repo path in subtree: %s", n.Path)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(res.Subtree)
}
