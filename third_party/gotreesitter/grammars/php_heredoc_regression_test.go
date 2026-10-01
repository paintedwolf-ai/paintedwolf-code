package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

func TestPHPHeredocStatementParsesWithoutError(t *testing.T) {
	src := []byte("<?php\necho <<<OMG\n  something\nOMG;\n")

	parser := ts.NewParser(PhpLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	root := tree.RootNode()
	if got, want := root.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("root end = %d, want %d; tree=%s", got, want, root.SExpr(PhpLanguage()))
	}
	if root.HasError() {
		t.Fatalf("root has error; tree=%s", root.SExpr(PhpLanguage()))
	}
	if got, want := root.ChildCount(), 2; got != want {
		t.Fatalf("root child count = %d, want %d; tree=%s", got, want, root.SExpr(PhpLanguage()))
	}
	if stmt := root.Child(1); stmt == nil || stmt.Type(PhpLanguage()) != "echo_statement" {
		t.Fatalf("child[1] = %v, want echo_statement; tree=%s", stmt, root.SExpr(PhpLanguage()))
	}
}
