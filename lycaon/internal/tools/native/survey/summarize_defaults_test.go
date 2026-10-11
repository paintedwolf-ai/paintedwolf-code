package survey

import (
	"context"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"strings"
	"testing"
)

func TestSummarizeDefaultFileRetainsSourceWithoutReferenceLeads(t *testing.T) {
	for _, tc := range []struct {
		path string
		body string
	}{
		{"pkg/target.go", "package pkg\n\nfunc Target() string { return \"useful detail\" }\n"},
		{"fixtures/target.json", "{\n  \"Target\": {\n    \"description\": \"useful detail\"\n  }\n}\n"},
		{"workflows/target.yaml", "Target:\n  description: useful detail\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tc.path, tc.body)
			writeFile(t, dir, "unrelated/caller.go", "package unrelated\nfunc Call() { Target() }\n")
			tool := testSummarizeTool(t, dir)
			tool.Catalog = sourcecatalog.New()
			raw, err := tool.Run(context.Background(), map[string]any{"path": tc.path}, nativefixture.Context(dir))
			testutil.FailErr(t, "summarize file with shipped caps", err)
			resp := decodeSummarizeResponse(t, raw)
			if len(resp.Pack.Identity) == 0 || len(resp.Pack.Skeleton) == 0 || len(resp.Anchors) == 0 {
				t.Fatalf("file briefing lost structure or anchors: %+v", resp)
			}
			retained := false
			for _, window := range resp.Pack.Substance {
				if window.Path != tc.path {
					t.Fatalf("source window escaped target: %+v", window)
				}
				retained = retained || strings.Contains(window.Body, "useful detail")
			}
			if !retained {
				t.Fatalf("target source detail missing: %+v", resp.Pack.Substance)
			}
			if len(resp.Pack.Neighbors)+len(resp.Pack.CallSites)+len(resp.Pack.Imports) != 0 {
				t.Fatalf("default summary gathered reference leads: %+v", resp.Pack)
			}
		})
	}
}

func TestSummarizeDefaultDirectoryKeepsDocRankingWithoutFanInScan(t *testing.T) {
	dir := t.TempDir()
	fanInTieFixture(t, dir)
	writeFile(t, dir, "README.md", "# App\n\nSee [beta](beta/b.go).\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	catalog := sourcecatalog.New()
	g.access.catalog, g.access.trees, g.access.literalsIndex = catalog, catalog.Trees, catalog.Literals
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Task: "explain beta"})
	testutil.FailErr(t, "gather directory with shipped caps", err)
	if res.Stats.FaninGrepPasses != 0 {
		t.Fatalf("default directory summary scanned repository fan-in: %+v", res.Stats)
	}
	docLinks := 0
	for path, importance := range res.Importance {
		if importance.FanIn != 0 {
			t.Fatalf("unexpected fan-in for %s: %+v", path, importance)
		}
		if path == "beta" || strings.HasPrefix(path, "beta/") {
			docLinks += importance.DocLinks
		}
	}
	if docLinks == 0 {
		t.Fatalf("documentation ranking lost: %+v", res.Importance)
	}
}
