package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

func TestPHPStaticAnonymousFunctionBeforeArrowStaysBounded(t *testing.T) {
	src := []byte("<?php\nstatic function () {}\nstatic fn () => 1;\n")
	parser := ts.NewParser(PhpLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	if got, want := tree.ParseRuntime().NodesAllocated, 1000; got > want {
		t.Fatalf("nodes allocated = %d, want <= %d; runtime=%s tree=%s", got, want, tree.ParseRuntime().Summary(), root.SExpr(PhpLanguage()))
	}
}
