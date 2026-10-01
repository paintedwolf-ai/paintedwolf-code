package survey

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSummarizeGatherDocLinkImportance(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "alpha/a.go", "package alpha\nfunc A() {}\n")
	writeFile(t, dir, "beta/b.go", "package beta\nfunc B() {}\n")
	writeFile(t, dir, "README.md", "# Root\n\nSee [beta](beta/b.go) for the core path.\n")

	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeDoclinkMax = 16
	caps.Pack.SubtreeFaninMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Task: "explain"})
	testutil.FailErr(t, "gather", err)
	if res.Subtree == nil {
		t.Fatal("expected Subtree")
	}
	if res.Importance == nil {
		t.Fatal("expected Importance from README doc-link")
	}
	found := false
	for path, c := range res.Importance {
		if c.DocLinks > 0 && (path == "beta" || strings.HasPrefix(path, "beta/") || path == "beta/b.go") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected doc-link boost under beta; got %+v", res.Importance)
	}
}

func TestSummarizeGatherImportanceDisabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "alpha/a.go", "package alpha\nfunc A() {}\n")
	writeFile(t, dir, "README.md", "See [alpha](alpha/a.go).\n")

	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeDoclinkMax = 0
	caps.Pack.SubtreeFaninMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Task: "explain"})
	testutil.FailErr(t, "gather", err)
	if res.Importance != nil {
		t.Fatalf("caps=0 must skip importance; got %+v", res.Importance)
	}
}
