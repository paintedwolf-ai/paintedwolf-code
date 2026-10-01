package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestParseFileMarkdownLinkReferenceDefinitionAllowsPunctuation(t *testing.T) {
	src := []byte("[a-b]: /x\n[a b]: /y\n")

	bt, err := ParseFile("refs.md", src)
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()

	root := bt.RootNode()
	if root == nil {
		t.Fatal("ParseFile returned nil root for markdown source")
	}
	if root.HasError() {
		t.Fatal("expected error-free markdown parse tree")
	}

	defs := 0
	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if bt.NodeType(node) == "link_reference_definition" {
			defs++
		}
		return gotreesitter.WalkContinue
	})
	if got, want := defs, 2; got != want {
		t.Fatalf("link_reference_definition count = %d, want %d", got, want)
	}
}

func TestParseFileMarkdownHeadingFieldStaysOnInlineChild(t *testing.T) {
	src := []byte("# Top\n\n## Tests\n")

	bt, err := ParseFile("heading.md", src)
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()

	lang := MarkdownLanguage()
	root := bt.RootNode()
	if root == nil {
		t.Fatal("ParseFile returned nil root for markdown heading")
	}
	if root.HasError() {
		t.Fatalf("expected error-free markdown parse tree, got %s", root.SExpr(lang))
	}

	var heading *gotreesitter.Node
	headingsSeen := 0
	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if bt.NodeType(node) == "atx_heading" {
			headingsSeen++
			if headingsSeen == 2 {
				heading = node
				return gotreesitter.WalkStop
			}
		}
		return gotreesitter.WalkContinue
	})
	if heading == nil {
		t.Fatal("expected to find nested atx_heading")
	}
	parent := heading.Parent()
	if parent == nil {
		t.Fatal("expected atx_heading to have a parent")
	}

	headingField := ""
	for i := 0; i < parent.ChildCount(); i++ {
		if parent.Child(i) == heading {
			headingField = parent.FieldNameForChild(i, lang)
			break
		}
	}
	if headingField != "" {
		t.Fatalf("atx_heading parent field = %q, want empty", headingField)
	}

	inlineField := ""
	for i := 0; i < heading.ChildCount(); i++ {
		if bt.NodeType(heading.Child(i)) == "inline" {
			inlineField = heading.FieldNameForChild(i, lang)
			break
		}
	}
	if got, want := inlineField, "heading_content"; got != want {
		t.Fatalf("inline field = %q, want %q", got, want)
	}
}
