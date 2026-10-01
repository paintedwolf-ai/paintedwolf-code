package survey

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNeighborsDocResolvableOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package pkg\nfunc A() {}\n")
	writeFile(t, dir, "docs/b.md", "# b\n")
	writeFile(t, dir, "docs/guide.md", "# Guide\n\nSee [a](../pkg/a.go) and `docs/b.md` plus `docs/nope.go`.\n")

	caps := summarize.DefaultCaps()
	caps.Gather.NeighborMax = 6
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "docs/guide.md", Task: "guide"})
	testutil.FailErr(t, "gather", err)
	if !res.Stats.PathIsFile {
		t.Fatalf("stats=%+v want PathIsFile", res.Stats)
	}
	paths := neighborPaths(res.Fit.Neighbors)
	if containsPath(paths, "docs/nope.go") {
		t.Fatalf("admitted nonexistent path: %v", paths)
	}
	if !containsPath(paths, "pkg/a.go") || !containsPath(paths, "docs/b.md") {
		t.Fatalf("neighbors = %v, want pkg/a.go and docs/b.md", paths)
	}
	if len(res.Fit.Neighbors) > caps.Gather.NeighborMax {
		t.Fatalf("neighbors = %d > NeighborMax", len(res.Fit.Neighbors))
	}
	if len(res.Fit.CallSites) != 0 {
		t.Fatalf("doc target should not gather call_sites; got %v", res.Fit.CallSites)
	}
}

func TestNeighborsDocCapped(t *testing.T) {
	dir := t.TempDir()
	var links strings.Builder
	links.WriteString("# many\n\n")
	for i := 0; i < 10; i++ {
		name := "docs/n" + string(rune('a'+i)) + ".md"
		writeFile(t, dir, name, "# n\n")
		links.WriteString("- [`" + name + "`](" + name + ")\n")
	}
	writeFile(t, dir, "docs/index.md", links.String())

	caps := summarize.DefaultCaps()
	caps.Gather.NeighborMax = 3
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "docs/index.md"})
	testutil.FailErr(t, "gather", err)
	if len(res.Fit.Neighbors) != 3 {
		t.Fatalf("neighbors = %d want 3; got %v", len(res.Fit.Neighbors), neighborPaths(res.Fit.Neighbors))
	}
}

func TestNeighborsCodeReferrers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/target.go", "package pkg\n\nfunc TargetFn() {}\n")
	writeFile(t, dir, "cmd/main.go", "package main\n\nimport \"example.com/app/pkg\"\n\nfunc main() { pkg.TargetFn() }\n")
	writeFile(t, dir, "other/use.go", "package other\n\nfunc Use() { TargetFn() }\n")
	writeFile(t, dir, "noise/x.go", "package noise\n\nfunc Unrelated() {}\n")

	caps := summarize.DefaultCaps()
	caps.Gather.NeighborMax = 6
	caps.Gather.CallSiteMax = 12
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/target.go", Task: "TargetFn"})
	testutil.FailErr(t, "gather", err)
	paths := neighborPaths(res.Fit.Neighbors)
	if containsPath(paths, "pkg/target.go") {
		t.Fatalf("neighbors include self: %v", paths)
	}
	if !containsPath(paths, "cmd/main.go") && !containsPath(paths, "other/use.go") {
		t.Fatalf("neighbors = %v, want callers", paths)
	}
	if len(res.Fit.Neighbors) > caps.Gather.NeighborMax {
		t.Fatalf("neighbors over cap: %d", len(res.Fit.Neighbors))
	}
	if len(res.Structure) != 1 || res.Structure[0].ImportPath == "" {
		t.Fatalf("expected ImportPath on structure; got %+v", res.Structure)
	}
}

func TestCallSitesCapped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/lib.go", "package pkg\n\nfunc Shared() {}\n")
	var body strings.Builder
	body.WriteString("package use\n\n")
	for i := 0; i < 20; i++ {
		body.WriteString("func F")
		body.WriteByte(byte('A' + i%26))
		body.WriteString("() { Shared() }\n")
	}
	writeFile(t, dir, "use/many.go", body.String())

	caps := summarize.DefaultCaps()
	caps.Gather.NeighborMax = 6
	caps.Gather.CallSiteMax = 4
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/lib.go"})
	testutil.FailErr(t, "gather", err)
	if len(res.Fit.CallSites) > 4 {
		t.Fatalf("call_sites = %d want ≤4", len(res.Fit.CallSites))
	}
	if len(res.Fit.CallSites) == 0 {
		t.Fatal("expected call_sites from Shared referrers")
	}
}

func TestNeighborsRankCompleteReferenceCounts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/target.go", "package pkg\nfunc shared() {}\n")
	writeFile(t, dir, ".scratch/reference.unknown", "shared\n")
	writeFile(t, dir, "a_first.go", referenceBody(400))
	writeFile(t, dir, "z_most.go", referenceBody(600))

	caps := summarize.DefaultCaps()
	caps.Gather.NeighborMax = 3
	caps.Gather.CallSiteMax = 0
	caps.Gather.ImportEdgeMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/target.go"})
	testutil.FailErr(t, "gather", err)
	paths := neighborPaths(res.Fit.Neighbors)
	if len(paths) != 3 || paths[0] != "z_most.go" || !containsPath(paths, ".scratch/reference.unknown") {
		t.Fatalf("neighbors = %+v", res.Fit.Neighbors)
	}
}

func referenceBody(count int) string {
	var body strings.Builder
	body.WriteString("package use\n")
	for i := range count {
		_, _ = fmt.Fprintf(&body, "func use%04d() { shared() }\n", i)
	}
	return body.String()
}

func TestCallSitesExcludeSelf(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/self.go", "package pkg\n\nfunc SelfFn() {}\n\nfunc other() { SelfFn() }\n")
	writeFile(t, dir, "cmd/main.go", "package main\n\nfunc main() { SelfFn() }\n")

	caps := summarize.DefaultCaps()
	caps.Gather.CallSiteMax = 12
	caps.Gather.NeighborMax = 6
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/self.go"})
	testutil.FailErr(t, "gather", err)
	for _, cs := range res.Fit.CallSites {
		if cs.Path == "pkg/self.go" {
			t.Fatalf("call_site includes self: %+v", cs)
		}
	}
	for _, n := range res.Fit.Neighbors {
		if n.Path == "pkg/self.go" {
			t.Fatalf("neighbor includes self: %+v", n)
		}
	}
}

func TestCallSitesStructuredTokensOnly(t *testing.T) {
	sc := summarize.StructureCandidate{
		RelPath:    "pkg/t.go",
		ImportPath: "example.com/app/pkg",
		Symbols: []summarize.StructureSymbol{
			{Kind: "func", Name: "Exported", Line: 3},
			{Kind: "func", Name: "unexported", Line: 5},
		},
	}
	tokens := fitGrepTokens(sc)
	want := map[string]bool{"example.com/app/pkg": true, "Exported": true, "unexported": true}
	for _, tok := range tokens {
		if !want[tok] {
			t.Fatalf("unexpected token %q in %v", tok, tokens)
		}
		delete(want, tok)
	}
	if len(want) != 0 {
		t.Fatalf("missing tokens %v; got %v", want, tokens)
	}
}

func TestIdentityOutlineMetadata(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/meta.go", "package pkg\n\nfunc Meta() {}\n")

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/meta.go"})
	testutil.FailErr(t, "gather", err)
	if len(res.Structure) != 1 {
		t.Fatalf("structure = %d", len(res.Structure))
	}
	sc := res.Structure[0]
	if sc.Language != "go" {
		t.Fatalf("language = %q want go", sc.Language)
	}
	if sc.OutlineSource == "" {
		t.Fatal("expected OutlineSource")
	}
	if sc.ImportPath != "example.com/app/pkg" {
		t.Fatalf("ImportPath = %q", sc.ImportPath)
	}
}

func TestFitSkippedForPatternAndDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package pkg\nfunc A() {}\n")
	writeFile(t, dir, "pkg/b.go", "package pkg\nfunc B() { A() }\n")

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	pat, err := g.Gather(context.Background(), summarize.Request{Path: "pkg", Pattern: "func"})
	testutil.FailErr(t, "pattern gather", err)
	if len(pat.Fit.Neighbors) != 0 || len(pat.Fit.CallSites) != 0 {
		t.Fatalf("pattern gather must skip fit; got %+v", pat.Fit)
	}
	dirRes, err := g.Gather(context.Background(), summarize.Request{Path: "pkg"})
	testutil.FailErr(t, "dir gather", err)
	if len(dirRes.Fit.Neighbors) != 0 || len(dirRes.Fit.CallSites) != 0 {
		t.Fatalf("dir gather must skip fit; got %+v", dirRes.Fit)
	}
}

func neighborPaths(ns []summarize.PackNeighbor) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Path)
	}
	return out
}
