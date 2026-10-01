//go:build !grammar_subset || grammar_subset_godot_resource

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func godotResourceParseTree(t *testing.T, src string) (*gotreesitter.Tree, *gotreesitter.Language) {
	t.Helper()
	lang := GodotResourceLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", src, err)
	}
	t.Cleanup(tree.Release)
	if tree.RootNode() == nil {
		t.Fatalf("Parse(%q) returned nil root", src)
	}
	return tree, lang
}

func godotResourceFindFirst(lang *gotreesitter.Language, n *gotreesitter.Node, typ string) *gotreesitter.Node {
	if n == nil {
		return nil
	}
	if n.Type(lang) == typ {
		return n
	}
	for i := 0; i < n.ChildCount(); i++ {
		if found := godotResourceFindFirst(lang, n.Child(i), typ); found != nil {
			return found
		}
	}
	return nil
}

// TestGodotResourceStringScannerMatchesC locks the external scanner to the
// pinned upstream scanner.c (tree-sitter-godot-resource @ 302c1895):
//   - leading whitespace is skipped (skip=true) before the opening quote
//   - a quote terminates the string only when the previous character is not
//     a backslash; upstream does NOT pair `\\`, so `\\"` keeps the string
//     open (the quote counts as escaped)
//   - unterminated strings produce no token
func TestGodotResourceStringScannerMatchesC(t *testing.T) {
	t.Run("simple string property", func(t *testing.T) {
		src := "[gd_resource]\nname = \"hello\"\n"
		tree, lang := godotResourceParseTree(t, src)
		want := `(resource (section (identifier) (property (path) (string))))`
		if got := tree.RootNode().SExpr(lang); got != want {
			t.Fatalf("SExpr mismatch:\n got  %s\n want %s", got, want)
		}
	})

	t.Run("escaped quote stays inside string", func(t *testing.T) {
		src := "[gd_resource]\nname = \"a\\\"b\"\n"
		tree, lang := godotResourceParseTree(t, src)
		if tree.RootNode().HasError() {
			t.Fatalf("unexpected error tree: %s", tree.RootNode().SExpr(lang))
		}
		str := godotResourceFindFirst(lang, tree.RootNode(), "string")
		if str == nil {
			t.Fatal("no string node found")
		}
		if got, want := src[str.StartByte():str.EndByte()], "\"a\\\"b\""; got != want {
			t.Fatalf("string span = %q, want %q", got, want)
		}
	})

	t.Run("double backslash quote does not terminate", func(t *testing.T) {
		// Upstream's last_char tracking treats the quote after `\\` as
		// escaped, so the string runs on to the next unescaped quote —
		// here the one that "opens" b.
		src := "[gd_resource]\nname = \"a\\\\\" + \"b\"\n"
		tree, lang := godotResourceParseTree(t, src)
		str := godotResourceFindFirst(lang, tree.RootNode(), "string")
		if str == nil {
			t.Fatal("no string node found")
		}
		if got, want := src[str.StartByte():str.EndByte()], "\"a\\\\\" + \""; got != want {
			t.Fatalf("string span = %q, want %q", got, want)
		}
	})

	t.Run("multiline string", func(t *testing.T) {
		src := "[gd_resource]\nname = \"line1\nline2\"\n"
		tree, lang := godotResourceParseTree(t, src)
		if tree.RootNode().HasError() {
			t.Fatalf("unexpected error tree: %s", tree.RootNode().SExpr(lang))
		}
		str := godotResourceFindFirst(lang, tree.RootNode(), "string")
		if str == nil {
			t.Fatal("no string node found")
		}
		if got, want := src[str.StartByte():str.EndByte()], "\"line1\nline2\""; got != want {
			t.Fatalf("string span = %q, want %q", got, want)
		}
	})
}
