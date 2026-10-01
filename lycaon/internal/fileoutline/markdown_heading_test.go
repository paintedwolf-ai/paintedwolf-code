package fileoutline

import (
	"strings"
	"testing"
)

func TestMarkdownHeadingNamesRecoverFromLine(t *testing.T) {
	src := "# Title\n\nbody\n\n### /cmd\n\nmore body here\n\n### a/b\n\nstill more\n\n## Permissions Section\n\ntail\n"
	got := map[string]bool{}
	for _, s := range AnalyzeText(t.Context(), "page.md", []byte(src)).Symbols {
		if s.Kind == "section" {
			got[s.Name] = true
		}
	}
	for _, want := range []string{"/cmd", "a/b", "Permissions Section", "Title"} {
		if !got[want] {
			t.Errorf("missing recovered heading %q; got %v", want, got)
		}
	}
	if got["/"] {
		t.Errorf("truncated %q heading should not appear: %v", "/", got)
	}
}

func TestMarkdownHeadingsRespectBlockStructure(t *testing.T) {
	got := AnalyzeText(t.Context(), "page.md", []byte("# Guide\n\ntext\n\nConfiguration\n---\n\n```md\n# Not a heading\n```\n")).Symbols
	if len(got) != 2 {
		t.Fatalf("symbols = %+v", got)
	}
	if got[0].Name != "Guide" || got[0].Line != 1 || got[1].Name != "Configuration" || got[1].Line != 5 {
		t.Fatalf("symbols = %+v", got)
	}
}

func TestMarkdownOutlineRetainsCompleteDefinitionsForLargeProse(t *testing.T) {
	src := []byte("# Start\n\n" + strings.Repeat("A paragraph with [a link](target.md), `code`, and **emphasis**.\n\n", 5000) + "## End\n")
	out := AnalyzeText(t.Context(), "guide.md", src)
	if err := out.DefinitionError(); err != nil {
		t.Fatalf("Markdown analysis failed: %v", err)
	}
	if out.Source != "markdown" || len(out.Symbols) != 2 || len(out.Definitions) != 2 || out.Symbols[1].Name != "End" {
		t.Fatalf("Markdown analysis source=%q symbols=%+v definitions=%d", out.Source, out.Symbols, len(out.Definitions))
	}
}
