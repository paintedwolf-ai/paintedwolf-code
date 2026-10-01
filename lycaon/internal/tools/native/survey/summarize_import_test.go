package survey

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestImportOutboundFromOutline(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/lib.go", "package pkg\n\nimport (\n\t\"fmt\"\n\t\"example.com/app/other\"\n)\n\nfunc Lib() { fmt.Println() }\n")
	writeFile(t, dir, "other/x.go", "package other\n\nvar X = 1\n")

	caps := summarize.DefaultCaps()
	caps.Gather.ImportEdgeMax = 16
	caps.Gather.NeighborMax = 0
	caps.Gather.CallSiteMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/lib.go"})
	testutil.FailErr(t, "gather", err)

	var outbound []summarize.PackImportEdge
	for _, e := range res.Fit.Imports {
		if e.Kind == "outbound" {
			outbound = append(outbound, e)
		}
	}
	if len(outbound) < 2 {
		t.Fatalf("outbound = %+v, want fmt + example.com/app/other", res.Fit.Imports)
	}
	got := map[string]bool{}
	for _, e := range outbound {
		if e.From != "pkg/lib.go" {
			t.Fatalf("outbound from = %q", e.From)
		}
		got[e.To] = true
	}
	if !got["fmt"] || !got["example.com/app/other"] {
		t.Fatalf("outbound tos = %v", got)
	}
}

func TestImportInboundCapped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/lib.go", "package pkg\n\nfunc Lib() {}\n")
	for i := 0; i < 8; i++ {
		name := string(rune('a' + i))
		writeFile(t, dir, "use/"+name+".go", "package use\n\nimport \"example.com/app/pkg\"\n\nfunc F() { _ = pkg.Lib }\n")
	}

	caps := summarize.DefaultCaps()
	caps.Gather.ImportEdgeMax = 3
	caps.Gather.NeighborMax = 0
	caps.Gather.CallSiteMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/lib.go"})
	testutil.FailErr(t, "gather", err)
	if len(res.Fit.Imports) > 3 {
		t.Fatalf("imports = %d want ≤3; %+v", len(res.Fit.Imports), res.Fit.Imports)
	}
	for _, e := range res.Fit.Imports {
		if e.Kind == "inbound" && e.From == "pkg/lib.go" {
			t.Fatalf("inbound includes self: %+v", e)
		}
		if e.Kind == "inbound" && e.To != "example.com/app/pkg" {
			t.Fatalf("inbound to = %q", e.To)
		}
	}
	inbound := 0
	for _, e := range res.Fit.Imports {
		if e.Kind == "inbound" {
			inbound++
		}
	}
	if inbound == 0 {
		t.Fatalf("expected inbound importers; got %+v", res.Fit.Imports)
	}
}

func TestImportDisabledByZero(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/lib.go", "package pkg\n\nimport \"fmt\"\n\nfunc Lib() { fmt.Println() }\n")
	writeFile(t, dir, "cmd/main.go", "package main\n\nimport \"example.com/app/pkg\"\n\nfunc main() { pkg.Lib() }\n")

	caps := summarize.DefaultCaps()
	caps.Gather.ImportEdgeMax = 0
	caps.Gather.NeighborMax = 0
	caps.Gather.CallSiteMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/lib.go"})
	testutil.FailErr(t, "gather", err)
	if len(res.Fit.Imports) != 0 {
		t.Fatalf("imports = %+v want empty when ImportEdgeMax=0", res.Fit.Imports)
	}
}

func TestImportNoMultiHop(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/a.go", "package pkg\n\nimport \"example.com/app/mid\"\n\nfunc A() {}\n")
	writeFile(t, dir, "mid/b.go", "package mid\n\nimport \"example.com/app/leaf\"\n\nfunc B() {}\n")
	writeFile(t, dir, "leaf/c.go", "package leaf\n\nfunc C() {}\n")

	caps := summarize.DefaultCaps()
	caps.Gather.ImportEdgeMax = 16
	caps.Gather.NeighborMax = 0
	caps.Gather.CallSiteMax = 0
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/a.go"})
	testutil.FailErr(t, "gather", err)
	for _, e := range res.Fit.Imports {
		if e.Kind == "outbound" && e.To == "example.com/app/leaf" {
			t.Fatalf("multi-hop outbound to leaf: %+v", res.Fit.Imports)
		}
	}
	foundMid := false
	for _, e := range res.Fit.Imports {
		if e.Kind == "outbound" && e.To == "example.com/app/mid" {
			foundMid = true
		}
	}
	if !foundMid {
		t.Fatalf("expected direct outbound to mid; got %+v", res.Fit.Imports)
	}
}

func TestImportDistinctFromCallSites(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/lib.go", "package pkg\n\nimport \"fmt\"\n\nfunc Shared() { fmt.Println() }\n")
	writeFile(t, dir, "cmd/main.go", "package main\n\nimport \"example.com/app/pkg\"\n\nfunc main() { pkg.Shared() }\n")

	caps := summarize.DefaultCaps()
	caps.Gather.ImportEdgeMax = 16
	caps.Gather.NeighborMax = 6
	caps.Gather.CallSiteMax = 12
	g := testSummarizeGatherer(t, dir, caps)
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/lib.go"})
	testutil.FailErr(t, "gather", err)
	if len(res.Fit.Imports) == 0 {
		t.Fatal("expected import edges")
	}
	if len(res.Fit.CallSites) == 0 && len(res.Fit.Neighbors) == 0 {
		t.Fatal("expected call_sites or neighbors from Shared referrers")
	}
	for _, e := range res.Fit.Imports {
		for _, cs := range res.Fit.CallSites {
			if e.From == cs.Path && e.To == cs.Excerpt {
				t.Fatalf("import edge collapsed into call_site: edge=%+v site=%+v", e, cs)
			}
		}
		if e.Kind == "" || (e.Kind != "outbound" && e.Kind != "inbound") {
			t.Fatalf("import kind = %q", e.Kind)
		}
	}
}
