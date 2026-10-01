//go:build !grammar_subset || grammar_subset_scheme

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestSchemeBackslashHashProducesErrorNode verifies that a token C rejects (a
// datum beginning with a bare backslash, e.g. "\#make-accessors") is surfaced
// as an ERROR node spanning the unlexable run, instead of go's previous over-
// lenient behavior of silently dropping the backslash and lexing the tail as a
// plain symbol. tree-sitter C produces:
//
//	(program (list "(" (ERROR) (symbol) (symbol) ")"))
//
// with the ERROR covering "\#make-accessors" (bytes 1-17) and the list keeping
// its opening parenthesis. Before the fix go produced (list "(" (symbol "make-accessors") ...).
func TestSchemeBackslashHashProducesErrorNode(t *testing.T) {
	lang := SchemeLanguage()
	src := []byte("(\\#make-accessors name slots)")

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if got := root.Type(lang); got != "program" {
		t.Fatalf("root type = %q, want %q (whole tree must not escalate to ERROR root): %s", got, "program", root.SExpr(lang))
	}

	list := root.NamedChild(0)
	if list == nil || list.Type(lang) != "list" {
		t.Fatalf("expected list as first datum, got %v: %s", list, root.SExpr(lang))
	}
	if got, want := list.StartByte(), uint32(0); got != want {
		t.Fatalf("list StartByte = %d, want %d (list must keep its opening paren)", got, want)
	}

	// First child of the list is the "(", second is the ERROR run.
	if list.ChildCount() < 2 {
		t.Fatalf("list has %d children, want >= 2: %s", list.ChildCount(), root.SExpr(lang))
	}
	errNode := list.Child(1)
	if errNode == nil || errNode.Type(lang) != "ERROR" {
		t.Fatalf("expected ERROR as list child 1, got %v: %s", errNode, root.SExpr(lang))
	}
	if got, want := errNode.StartByte(), uint32(1); got != want {
		t.Fatalf("ERROR StartByte = %d, want %d (the backslash, matching C)", got, want)
	}
	if got, want := errNode.EndByte(), uint32(17); got != want {
		t.Fatalf("ERROR EndByte = %d, want %d (covers \\#make-accessors, matching C)", got, want)
	}
	if got, want := errNode.Text(src), "\\#make-accessors"; got != want {
		t.Fatalf("ERROR text = %q, want %q", got, want)
	}

	// The two trailing symbols must lex normally after the error run.
	wantSymbols := []struct {
		text       string
		start, end uint32
	}{
		{"name", 18, 22},
		{"slots", 23, 28},
	}
	for i, ws := range wantSymbols {
		child := list.Child(2 + i)
		if child == nil || child.Type(lang) != "symbol" {
			t.Fatalf("list child %d = %v, want symbol %q", 2+i, child, ws.text)
		}
		if child.StartByte() != ws.start || child.EndByte() != ws.end {
			t.Fatalf("symbol %q span = [%d-%d], want [%d-%d]", ws.text, child.StartByte(), child.EndByte(), ws.start, ws.end)
		}
	}
}

// TestSchemeValidInputUnaffectedByErrorRun guards against the error-run lexing
// firing on legitimate scheme, including pipe-delimited symbols and datum
// comments, which must continue to parse without any ERROR node.
func TestSchemeValidInputUnaffectedByErrorRun(t *testing.T) {
	lang := SchemeLanguage()
	cases := []string{
		"(define |foo bar| 1)",
		"#;(skipped datum) (real)",
		"`(a ,b ,@c)",
		"|a\\x41;b|",
		"(let ((x 1)) x)",
	}
	for _, src := range cases {
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse %q failed: %v", src, err)
		}
		if tree.RootNode().HasError() {
			t.Fatalf("valid scheme %q produced an error tree: %s", src, tree.RootNode().SExpr(lang))
		}
	}
}
