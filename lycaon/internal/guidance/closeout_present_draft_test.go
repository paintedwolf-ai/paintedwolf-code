package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/navigationref"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

func presentationLedger() evidence.Ledger {
	return evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "internal/a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 90}},
			Body: []string{"60|the anchored statement"}},
		{Handle: "read#4", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "docs/a b.md", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
		{Handle: "read#5", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "LICENSE", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
		{Handle: "grep#1", Kind: "grep", Shape: evidence.ShapeOpaque, Path: "."},
		{Handle: "task#1", Kind: "task", Shape: evidence.ShapeOpaque},
		{Handle: "command#4", Kind: "command", Shape: evidence.ShapeCommand},
		{Handle: "fetch#1", Kind: "fetch", Shape: evidence.ShapeURL, URL: "https://example.test/seen"},
	})
}

func present(source string, ev evidence.Ledger) string {
	roots := evidence.CitationRoots{}
	draft := guidance.ParseCloseoutDraft(source, citationmd.Options{KnownPath: guidance.KnownLedgerPath(roots, ev)})
	return guidance.PresentCloseoutMarkdown(draft, guidance.ResolveDraftCitations(roots, "implement_synthesis", draft, guidance.CloseoutEvidence{Ledger: ev}, nil))
}

func TestPresentCloseoutMarkdown(t *testing.T) {
	t.Parallel()
	fence := "```\n[read#3:42] [verify: targeted - x] [gone#1]\n```"
	cases := []struct{ name, source, want string }{
		{"handle becomes a path link", "Timeout at [read#3:42-48].", "Timeout at [internal/a.go:42-48](internal/a.go#L42-L48)."},
		{"path citation", "See [internal/a.go:7].", "See [internal/a.go:7](internal/a.go#L7)."},
		{"bare path", "See [internal/a.go].", "See [internal/a.go](internal/a.go)."},
		{"bare handle with a path", "See [read#3].", "See [internal/a.go](internal/a.go)."},
		{"known name", "See [LICENSE].", "See [LICENSE](LICENSE)."},
		{"quoted path", `See ["docs/a b.md":3].`, "See [docs/a b.md:3](<docs/a b.md#L3>)."},
		{"validated coordinates, not the written ones", `At [internal/a.go:40 "the anchored statement"].`, "At [internal/a.go:60](internal/a.go#L60)."},
		{"pathless handle leaves its prose", "I dispatched a worker [task#1], then waited.", "I dispatched a worker, then waited."},
		{"search root is not a path", "No matches [grep#1] anywhere.", "No matches anywhere."},
		{"group", "Both [read#3:1, task#1, internal/a.go:2] agree.", "Both [internal/a.go:1](internal/a.go#L1), [internal/a.go:2](internal/a.go#L2) agree."},
		{"group of nothing", "Done [task#1, command#4].", "Done."},
		{"verify line goes", "Fixed.\n[verify: targeted - Checked it] [command#4]\n", "Fixed."},
		{"directive paragraph goes", "Fixed.\n\n[verify: targeted - Checked it]\n\nNext.", "Fixed.\n\nNext."},
		{"artifact goes", "See it. [artifact: 11111111-1111-1111-1111-111111111111]", "See it."},
		{"observed link stays", "Per [spec](https://example.test/seen).", "Per [spec](https://example.test/seen)."},
		{"unobserved link keeps its label", "Per [spec](https://example.test/other).", "Per spec."},
		{"unobserved autolink cannot relink", "Per <https://example.test/other>.", "Per `https://example.test/other`."},
		{"workspace link stays", "Per [auth](internal/a.go#L4).", "Per [auth](internal/a.go#L4)."},
		{"unobserved workspace link keeps its label", "Per [auth](internal/z.go#L4).", "Per auth."},
		{"AC-AST-07 unresolved reference goes", "Claim [read#99:2] stands.", "Claim stands."},
		{"unsupported range goes", "Claim [read#4:2-50] stands.", "Claim stands."},
		{"damaged node goes", "Claim [read#x] stands [verify: nope].", "Claim stands."},
		{"AC-AST-14 fence is byte for byte", "Syntax:\n\n" + fence + "\n\nEnd [task#1].", "Syntax:\n\n" + fence + "\n\nEnd."},
		{"AC-AST-14 inline code", "Write `[read#1]` to cite.", "Write `[read#1]` to cite."},
		{"AC-AST-05 prose brackets", "A [sic] and [draft] note.", "A [sic] and [draft] note."},
		{"a sources heading is prose", "Sources:\n\n- [read#3:1]\n- plain\n", "Sources:\n\n- [internal/a.go:1](internal/a.go#L1)\n- plain"},
		{"handle text elsewhere is prose", "The word read#3 stays.", "The word read#3 stays."},
		{"no protocol at all", "Just\n\n  text.", "Just\n\n  text."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := present(tc.source, presentationLedger()); got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// With nothing for the ledger to add, acceptance equals the streamed projection.
func TestPresentationAgreesWithStream(t *testing.T) {
	t.Parallel()
	source := "Intro [task#1].\n\n```\n[read#1]\n```\n\nSee [internal/a.go:7] and [spec](https://example.test/seen) and `x`.\n[verify: targeted - y] [command#4]\n"
	projector := citationmd.NewProjector(citationmd.Options{})
	stream := projector.Write(source) + projector.Close()
	if accepted := present(source, presentationLedger()); accepted != stream {
		t.Fatalf("accepted %q\nstream %q", accepted, stream)
	}
}

// The rewritten form navigates through the host's own reference parser.
func TestPresentedPathLinksNavigate(t *testing.T) {
	t.Parallel()
	p := &project.Project{ID: "p", Roots: []project.Root{
		{ID: "main", Label: "main", Path: "/work/main", IsPrimary: true},
		{ID: "lib", Label: "shared", Path: "/work/shared"},
	}}
	cases := []struct {
		name      string
		link      string
		root      string
		path      string
		line, end int
	}{
		{"line", citationmd.FileLink("internal/a.go", 42, 0), "main", "internal/a.go", 42, 0},
		{"range", citationmd.FileLink("internal/a.go", 42, 58), "main", "internal/a.go", 42, 58},
		{"bare", citationmd.FileLink("internal/a.go", 0, 0), "main", "internal/a.go", 0, 0},
		{"space", citationmd.FileLink("docs/a b.md", 3, 0), "main", "docs/a b.md", 3, 0},
		{"root label", citationmd.FileLink("@shared/lib/sec.go", 12, 0), "lib", "lib/sec.go", 12, 0},
		{"unicode", citationmd.FileLink("docs/設計.md", 7, 0), "main", "docs/設計.md", 7, 0},
		{"reserved byte", citationmd.FileLink("docs/c#.md", 1, 0), "main", "docs/c#.md", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			refs := navigationref.BuildProjectPathReferences(p, "See "+tc.link+" here.", nil)
			if len(refs) != 1 {
				t.Fatalf("refs = %+v", refs)
			}
			ref := refs[0]
			if ref.Syntax != "link" || !ref.Explicit || ref.RootID != tc.root || ref.Path != tc.path || ref.Line != tc.line || ref.EndLine != tc.end {
				t.Fatalf("ref = %+v", ref)
			}
			if ref.Status == api.NavigationUnavailable {
				t.Fatalf("unavailable: %+v", ref)
			}
		})
	}
	if strings.Contains(citationmd.FileLink("a b.go", 1, 0), "%20") {
		t.Fatal("a space must stay raw so that Den's citation index can match the path")
	}
}
