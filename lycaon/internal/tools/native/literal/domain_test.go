package literal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
)

const domainSource = "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"a\") }\n\nfunc helper() int { return 1 }\n"

func domainHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, map[string]string{"main.go": domainSource, "pkg/store.go": "package pkg\n// store\n", "README.md": "# fixture\n"})
	caps := summarize.DefaultCaps()
	caps.Gather.IndexWaitMs = 30_000
	catalog, err := reposurvey.LoadCatalog(reposurvey.CatalogDir())
	testutil.FailErr(t, "survey catalog", err)
	sources := sourcecatalog.New()
	_, err = sources.Snapshot(t.Context(), "", []sourcecatalog.Root{{ID: "r1", Path: h.root}})
	testutil.FailErr(t, "warm source catalog", err)
	boundary := nativefixture.Boundary(t)
	h.owners.Domain = DomainOwners{
		Summarize: &survey.SummarizeTool{Boundary: boundary, Caps: caps},
		Repo:      &reposurvey.RepoTool{Boundary: boundary, BaseCatalog: catalog, Caps: reposurvey.DefaultCaps(), SourceCatalog: sources},
	}
	return h
}

// TestOutlineBuiltinSharesTheReadOwnerAndPinsItsSource: outline emits the
// same typed outline the read tool presents, over a pinned capture.
func TestOutlineBuiltinSharesTheReadOwnerAndPinsItsSource(t *testing.T) {
	h := domainHarness(t)
	r := h.exec("", "outline", "--path", "main.go")
	if r.err != nil || r.result.Status != 0 || !strings.HasSuffix(r.stdout, "\n") {
		t.Fatalf("outline = %+v %v %q", r.result, r.err, r.stdout)
	}
	var resp survey.ReadResponse
	testutil.FailErr(t, "decode outline", json.Unmarshal([]byte(r.stdout), &resp))
	if resp.Mode != "outline" || !strings.Contains(r.stdout, `"helper"`) || !strings.Contains(r.stdout, `"main"`) {
		t.Fatalf("outline response = %s", r.stdout)
	}
	pins := h.pins.pins[r.execution]
	if len(pins) != 1 || pins[0].Path != "main.go" || pins[0].Representation != toolcontract.OriginalEncodedBytes || pins[0].ContentBytes != int64(len(domainSource)) {
		t.Fatalf("outline pins = %+v", pins)
	}
}

// TestSummarizeAndRepoMapBuiltinsUseTheirDomainOwners: both built-ins run
// the retained structured operations and print their structured result.
func TestSummarizeAndRepoMapBuiltinsUseTheirDomainOwners(t *testing.T) {
	h := domainHarness(t)
	r := h.exec("", "summarize", "--path", "main.go", "--focus", "entry point")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("summarize = %+v %v", r.result, r.err)
	}
	var brief survey.SummarizeResponse
	testutil.FailErr(t, "decode summarize", json.Unmarshal([]byte(r.stdout), &brief))
	if brief.Task != "entry point" || len(brief.Pack.Identity) == 0 || brief.Pack.Identity[0].Path != "main.go" {
		t.Fatalf("summarize response = %s", r.stdout)
	}
	pins := h.pins.pins[r.execution]
	if len(pins) == 0 {
		t.Fatal("summarize read files without pinning them")
	}
	sum := textfile.SHA256([]byte(domainSource))
	found := false
	for _, pin := range pins {
		if pin.Path == "main.go" {
			found = pin.Mode == toolcontract.PublishedDiskRevision && pin.DiskSHA256 == sum && pin.ContentBytes == int64(len(domainSource))
		}
		if seen := strings.Count(strings.Join(pinPaths(pins), "\n"), pin.Path); seen != 1 {
			t.Fatalf("pin for %s recorded %d times", pin.Path, seen)
		}
	}
	if !found {
		t.Fatalf("summarize pins = %+v", pins)
	}
	m := h.exec("", "repo-map")
	if got := h.pins.pins[m.execution]; len(got) != 0 {
		t.Fatalf("repo-map reads metadata only, yet pinned %+v", got)
	}
	if m.err != nil || m.result.Status != 0 {
		t.Fatalf("repo-map = %+v %v", m.result, m.err)
	}
	var layout reposurvey.RepoResponse
	testutil.FailErr(t, "decode repo-map", json.Unmarshal([]byte(m.stdout), &layout))
	if layout.Bundle != "layout_overview" || layout.Digest == "" {
		t.Fatalf("repo-map response = %s", m.stdout)
	}
	scoped := h.exec("", "repo-map", "--path", "pkg")
	if scoped.err != nil || !strings.Contains(scoped.stdout, "pkg") {
		t.Fatalf("scoped repo-map = %+v %v %s", scoped.result, scoped.err, scoped.stdout)
	}
	unavailable := newHarness(t, nil)
	unavailable.exec("", "summarize", "--path", ".").refused(t, "summarize without owner", "COMMAND_PLATFORM_UNAVAILABLE")
	unavailable.exec("", "repo-map").refused(t, "repo-map without owner", "COMMAND_PLATFORM_UNAVAILABLE")
}

func pinPaths(pins []toolcontract.ExecutionRevision) []string {
	out := make([]string, len(pins))
	for i, pin := range pins {
		out[i] = pin.Path
	}
	return out
}
