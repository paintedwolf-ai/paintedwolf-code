package navigationref

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNavigationParsingNeverNeedsFilesystem(t *testing.T) {
	p := &project.Project{ID: "project", Roots: []project.Root{{ID: "root", Label: "main", Path: "/does/not/exist", IsPrimary: true}}}
	prose := "See `a.go`, `.hidden/ignored.json`, [first](one/config.yaml), [second](two/config.yaml), and [space](<docs/My File.md>)."
	refs := BuildProjectPathReferences(p, prose, nil)
	if len(refs) != 4 {
		t.Fatalf("refs = %+v", refs)
	}
	for _, ref := range refs {
		if ref.Status != api.NavigationPending {
			t.Fatalf("unexpected resolution: %+v", ref)
		}
		if ref.Mention == "a.go" {
			t.Fatalf("unbound basename became a reference: %+v", ref)
		}
		if ref.RootID != "root" || (ref.Mention != ".hidden/ignored.json" && !ref.Explicit) {
			t.Fatalf("explicit destination lost: %+v", ref)
		}
	}
}

func TestNavigationParsingPreservesDestinationsAndScope(t *testing.T) {
	p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", Label: "backend", IsPrimary: true}}}
	refs := BuildProjectPathReferences(p, "[file](@backend/src/a.go#L10-L20) `folder/` [file](docs/a%20b.md)", nil)
	got := map[string]api.NavigationReference{}
	for _, r := range refs {
		got[r.Mention] = r
	}
	ref := got["@backend/src/a.go#L10-L20"]
	if ref.RootID != "r" || ref.Path != "src/a.go" || ref.Line != 10 || ref.EndLine != 20 {
		t.Fatalf("qualified ref: %+v", ref)
	}
	if got["docs/a%20b.md"].Path != "docs/a b.md" {
		t.Fatalf("escaped path: %+v", got)
	}
	if got["folder/"].Explicit {
		t.Fatal("folder basename became an exact root path")
	}
}

func TestNavigationParsingExcludesForeignMarkupAndCode(t *testing.T) {
	prose := "[web](https://example.com/a.go) ![image](image.png) <span>hidden.go</span>\n\n```go\nexample.go\n```\n\n`../outside.go` [escape](/tmp/a.go)"
	refs := BuildProjectPathReferences(&project.Project{ID: "p"}, prose, nil)
	for _, ref := range refs {
		if ref.Mention != "hidden.go" {
			t.Fatalf("unexpected reference: %+v", ref)
		}
	}
}

func TestNavigationParsingIsBounded(t *testing.T) {
	var prose strings.Builder
	for i := 0; i < api.MaxMessageNavigationRefs+20; i++ {
		fmt.Fprintf(&prose, " [file](file-%d.go)", i)
	}
	refs := BuildProjectPathReferences(&project.Project{ID: "p"}, prose.String(), nil)
	if len(refs) != api.MaxMessageNavigationRefs {
		t.Fatalf("count=%d", len(refs))
	}
}

func TestNavigationReusableDestinationsAndAbsolutePaths(t *testing.T) {
	p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", Path: "/repo", IsPrimary: true}}}
	refs := BuildProjectPathReferences(p, "[a](source://r/docs/My%20File.md#L4-L8) [b](/repo/.hidden/file.go) [c](file:///repo/ignored/a.go) [outside](/other/a.go)", nil)
	if len(refs) != 3 {
		t.Fatalf("refs=%+v", refs)
	}
	for _, ref := range refs {
		if !ref.Explicit || ref.RootID != "r" {
			t.Fatalf("lost root: %+v", ref)
		}
	}
	for _, ref := range refs {
		if strings.HasPrefix(ref.Mention, "source:") && (ref.Path != "docs/My File.md" || ref.Line != 4 || ref.EndLine != 8) {
			t.Fatalf("lost source location: %+v", ref)
		}
	}
}

func TestNavigationExtensionlessCodeAndLink(t *testing.T) {
	p := &project.Project{ID: "p"}
	refs := BuildProjectPathReferences(p, "Read `LICENSE` and [build](Makefile). Ordinary words are not references.", &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: "p", RootID: "r", Path: "LICENSE", EntryKind: api.NavigationEntryKindFile}}})
	if len(refs) != 2 {
		t.Fatalf("refs=%+v", refs)
	}
	for _, ref := range refs {
		if ref.Path != "LICENSE" && ref.Path != "Makefile" {
			t.Fatalf("unexpected ref=%+v", ref)
		}
	}
}

func TestNavigationRootAliasesAndEscapedFilenameSyntax(t *testing.T) {
	p := &project.Project{ID: "p", Roots: []project.Root{
		{ID: "r", Label: "main", Path: "/primary", IsPrimary: true},
		{ID: "r", Label: "main", Path: "/branch", IsPrimary: true},
		{ID: "r", Label: "main", Path: "/primary", IsPrimary: true},
	}}
	refs := BuildProjectPathReferences(p, "[root](@main/a.go) [absolute](/primary/a.go) [colon](source://r/file:12) [hash](source://r/a%23L12) [at](source://r/@dir/a.go#L4)", nil)
	if len(refs) != 5 {
		t.Fatalf("refs=%+v", refs)
	}
	for _, ref := range refs {
		if strings.Contains(ref.Mention, "file:12") && (ref.Path != "file:12" || ref.Line != 0) {
			t.Fatalf("colon filename=%+v", ref)
		}
		if strings.Contains(ref.Mention, "%23") && (ref.Path != "a#L12" || ref.Line != 0) {
			t.Fatalf("hash filename=%+v", ref)
		}
		if strings.Contains(ref.Mention, "@dir") && (ref.Path != "@dir/a.go" || ref.Line != 4) {
			t.Fatalf("at filename=%+v", ref)
		}
	}
}

func TestNavigationSourceDestinationPreservesWorkerIdentity(t *testing.T) {
	refs := BuildProjectPathReferences(&project.Project{ID: "p"}, "[worker](source://r/src/a.go?job_id=worker-1#L12) [invalid](source://r/a.go?unknown=x)", nil)
	if len(refs) != 1 || refs[0].WorkerID != "worker-1" || refs[0].Path != "src/a.go" || refs[0].Line != 12 {
		t.Fatalf("refs=%+v", refs)
	}
}

func TestNavigationBareExtensionlessWordInTextNeverBecomesReference(t *testing.T) {
	p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", Path: "/repo", IsPrimary: true}}}
	context := &api.SourceContext{Locations: []api.NavigationTarget{
		{ProjectID: "p", RootID: "r", Path: "task", EntryKind: api.NavigationEntryKindFile},
		{ProjectID: "p", RootID: "r", Path: "main.go", EntryKind: api.NavigationEntryKindFile},
	}}

	// Plain prose ignores bare extensionless words, while paths with extensions match.
	prose := "Our primary task is to update main.go before this task is finished."
	refs := BuildProjectPathReferences(p, prose, context)
	if len(refs) != 1 || refs[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got: %+v", refs)
	}

	// Backticks, path prefixes, and links produce references.
	syntacticProse := "Run `task` or ./task or [view](task) for details."
	synRefs := BuildProjectPathReferences(p, syntacticProse, context)
	if len(synRefs) != 3 {
		t.Fatalf("expected 3 references for syntactic forms, got: %+v", synRefs)
	}
	for _, ref := range synRefs {
		if ref.Path != "task" {
			t.Fatalf("unexpected target path: %+v", ref)
		}
	}
	if synRefs[0].Syntax != "code" || synRefs[1].Syntax != "text" || synRefs[2].Syntax != "link" {
		t.Fatalf("unexpected syntaxes: %+v", synRefs)
	}
}

func TestNavigationParsedPathsAlwaysEncode(t *testing.T) {
	p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", Label: "main", Path: "/repo", IsPrimary: true}}}
	prose := "`GET /` returns 200. Also `POST /` and [trail](foo%20/) [lead](%20foo) [tab](a%09/) `src/ok.go` [fine](docs/a%20b.md)."
	refs := BuildProjectPathReferences(p, prose, nil)
	if _, err := sourceref.EncodeMetadata(refs, nil); err != nil {
		t.Fatalf("parsed references must satisfy the stored-path rule: %v (refs=%+v)", err, refs)
	}
	got := map[string]bool{}
	for _, ref := range refs {
		got[ref.Path] = true
	}
	if !got["src/ok.go"] || !got["docs/a b.md"] {
		t.Fatalf("valid paths lost: %+v", refs)
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref.Path) != ref.Path {
			t.Fatalf("padded path survived parsing: %+v", ref)
		}
	}
}
