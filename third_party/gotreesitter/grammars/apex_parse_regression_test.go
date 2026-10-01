package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

func TestApexGenericLocalDeclarationMatchesCShape(t *testing.T) {
	src := []byte("public class C {\n  void m() {\n    List<List<SObject>> searchResults = [FIND :keyword IN ALL FIELDS];\n  }\n}\n")
	parser := ts.NewParser(ApexLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	local := findFirstApexNode(root, ApexLanguage(), "local_variable_declaration")
	if local == nil {
		t.Fatalf("missing local_variable_declaration: %s", root.SExpr(ApexLanguage()))
	}
	if got := local.ChildCount(); got != 3 {
		t.Fatalf("local_variable_declaration child count = %d, want 3: %s", got, local.SExpr(ApexLanguage()))
	}
	if typ := local.Child(0); typ == nil || typ.Type(ApexLanguage()) != "generic_type" {
		t.Fatalf("local child[0] = %v, want generic_type: %s", typ, local.SExpr(ApexLanguage()))
	}
	if decl := local.Child(1); decl == nil || decl.Type(ApexLanguage()) != "variable_declarator" {
		t.Fatalf("local child[1] = %v, want variable_declarator: %s", decl, local.SExpr(ApexLanguage()))
	}
}

func TestApexClassLiteralAccessMatchesCShape(t *testing.T) {
	src := []byte("public class C {\n  void m() {\n    Object t = RecordPage.class;\n  }\n}\n")
	parser := ts.NewParser(ApexLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	access := findFirstApexNode(root, ApexLanguage(), "field_access")
	if access == nil {
		t.Fatalf("missing field_access: %s", root.SExpr(ApexLanguage()))
	}
	if got := access.ChildCount(); got != 3 {
		t.Fatalf("field_access child count = %d, want 3: %s", got, access.SExpr(ApexLanguage()))
	}
	if left := access.Child(0); left == nil || left.Type(ApexLanguage()) != "identifier" || string(left.Text(src)) != "RecordPage" {
		t.Fatalf("field_access child[0] = %v, want RecordPage identifier: %s", left, access.SExpr(ApexLanguage()))
	}
	if right := access.Child(2); right == nil || right.Type(ApexLanguage()) != "identifier" || string(right.Text(src)) != "class" {
		t.Fatalf("field_access child[2] = %v, want class identifier: %s", right, access.SExpr(ApexLanguage()))
	}
}

func findFirstApexNode(n *ts.Node, lang *ts.Language, typ string) *ts.Node {
	if n == nil {
		return nil
	}
	if n.Type(lang) == typ {
		return n
	}
	for i := 0; i < n.ChildCount(); i++ {
		if found := findFirstApexNode(n.Child(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}
