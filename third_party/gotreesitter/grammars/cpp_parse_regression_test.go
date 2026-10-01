package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

func TestCppUserTypeTemplateArgumentInParameterListParsesWithoutError(t *testing.T) {
	src := []byte("#include <vector>\nstruct Rule {};\nstatic inline void f(std::vector<Rule> *v) {}\n")
	parser := ts.NewParser(CppLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if root.HasError() {
		t.Fatalf("unexpected parse errors: %s runtime=%s", root.SExpr(CppLanguage()), tree.ParseRuntime().Summary())
	}
}
