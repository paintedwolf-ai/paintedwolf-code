package literal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/pkg/api"
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

// The form carries every argument the tool ships with.
func TestSummarizeFormCarriesShippedArguments(t *testing.T) {
	h := domainHarness(t)
	several := h.exec("", "summarize", "--path", "main.go", "--path", "pkg/store.go", "--pattern", "package")
	if several.err != nil || several.result.Status != 0 {
		t.Fatalf("several paths = %+v %v", several.result, several.err)
	}
	var brief survey.SummarizeResponse
	testutil.FailErr(t, "decode several paths", json.Unmarshal([]byte(several.stdout), &brief))
	seen := map[string]bool{}
	for _, identity := range brief.Pack.Identity {
		seen[identity.Path] = true
	}
	if !seen["main.go"] || !seen["pkg/store.go"] {
		t.Fatalf("identity = %+v", brief.Pack.Identity)
	}
	inline := h.exec("", "summarize", "--content", "supplied text")
	if inline.err != nil || inline.result.Status != 0 {
		t.Fatalf("inline content = %+v %v", inline.result, inline.err)
	}
	if empty := h.exec("", "summarize"); empty.err == nil && empty.result.Status == 0 {
		t.Fatal("summarize ran with no path or content")
	}
}

func TestRepoMapSelectsItsBundle(t *testing.T) {
	h := domainHarness(t)
	r := h.exec("", "repo-map", "--bundle", "api_routes")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("repo-map --bundle = %+v %v", r.result, r.err)
	}
	var result reposurvey.RepoResponse
	testutil.FailErr(t, "decode repo-map", json.Unmarshal([]byte(r.stdout), &result))
	if result.Bundle != "api_routes" {
		t.Fatalf("bundle = %q", result.Bundle)
	}
}

type stubDomainEditorDocs struct {
	tools.EditorDocuments
	doc  tools.EditorDocumentText
	open bool
	err  error
}

func (s *stubDomainEditorDocs) OpenDocument(context.Context, string, sourcebranch.ID, string, string) (tools.EditorDocumentText, bool, error) {
	return s.doc, s.open, s.err
}

func TestCaptureWindowDocumentIdentity(t *testing.T) {
	rootDir := t.TempDir()
	docText := "alpha\nbeta\ngamma\n"
	lines := toolkit.SplitLines(docText)
	fullBody := hostmarker.FormatNumberedLines(lines, 1)

	makeTC := func(docs tools.EditorDocuments, wsKind api.SourceWorkspaceKind) tools.ToolContext {
		return tools.ToolContext{
			Identity: tools.InvocationIdentity{ProjectID: "proj-1", SessionID: "sess-1"},
			Source: tools.InvocationSource{
				ActiveRootID:        "r1",
				SourceWorkspaceKind: wsKind,
				Roots:               []projectroot.RootRef{{ID: "r1", Path: rootDir}},
				EditorDocuments:     docs,
			},
		}
	}

	docs := &stubDomainEditorDocs{
		doc:  tools.EditorDocumentText{ID: "doc-1", Revision: 4, Text: docText},
		open: true,
	}
	tc := makeTC(docs, api.SourceWorkspaceKindProject)

	if id, rev := captureWindowDocumentIdentity(t.Context(), tc, "r1", "target.txt", "target.txt", 1, len(lines), fullBody); id != "doc-1" || rev != 4 {
		t.Fatalf("exact whole window = (%q, %d), want (doc-1, 4)", id, rev)
	}
	if id, rev := captureWindowDocumentIdentity(t.Context(), tc, "missing-root", "target.txt", "target.txt", 1, len(lines), fullBody); id != "" || rev != 0 {
		t.Fatalf("missing root = (%q, %d), want empty", id, rev)
	}
	if id, rev := captureWindowDocumentIdentity(t.Context(), tc, "r1", "target.txt", "target.txt", 1, len(lines)-1, fullBody); id != "" || rev != 0 {
		t.Fatalf("partial endLine = (%q, %d), want empty", id, rev)
	}
	if id, rev := captureWindowDocumentIdentity(t.Context(), tc, "r1", "target.txt", "target.txt", 1, len(lines), "1\tchanged"); id != "" || rev != 0 {
		t.Fatalf("mismatched body = (%q, %d), want empty", id, rev)
	}
	closedTC := makeTC(&stubDomainEditorDocs{open: false}, api.SourceWorkspaceKindProject)
	if id, rev := captureWindowDocumentIdentity(t.Context(), closedTC, "r1", "target.txt", "target.txt", 1, len(lines), fullBody); id != "" || rev != 0 {
		t.Fatalf("closed doc = (%q, %d), want empty", id, rev)
	}
	badBranchTC := makeTC(docs, api.SourceWorkspaceKind("invalid"))
	if id, rev := captureWindowDocumentIdentity(t.Context(), badBranchTC, "r1", "target.txt", "target.txt", 1, len(lines), fullBody); id != "" || rev != 0 {
		t.Fatalf("invalid source branch = (%q, %d), want empty", id, rev)
	}
}
