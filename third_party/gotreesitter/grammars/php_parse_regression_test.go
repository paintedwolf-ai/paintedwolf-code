package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

func TestPHPMixedGroupedUseRetainsNamespaceUseDeclaration(t *testing.T) {
	src := []byte("<?php\nuse Foo\\Baz\\{\n  Bar as Barr,\n  function foo as fooo,\n  const FOO as FOOO,\n};\n")
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
	if got := root.EndByte(); got != uint32(len(src)) {
		t.Fatalf("root end = %d, want %d; tree=%s", got, len(src), root.SExpr(PhpLanguage()))
	}
	if got := root.ChildCount(); got != 2 {
		t.Fatalf("root child count = %d, want 2; tree=%s", got, root.SExpr(PhpLanguage()))
	}
	if decl := root.Child(1); decl == nil || decl.Type(PhpLanguage()) != "namespace_use_declaration" {
		t.Fatalf("second child = %v, want namespace_use_declaration; tree=%s", decl, root.SExpr(PhpLanguage()))
	} else if !decl.HasError() {
		t.Fatalf("grouped use should retain error flag for trailing comma recovery; tree=%s", root.SExpr(PhpLanguage()))
	}
}

func TestPHPGroupedUseRecoveryPreservesFollowingFunction(t *testing.T) {
	src := []byte("<?php\nnamespace A;\n\nuse Foo\\Baz as Baaz;\n\nuse Foo\\Baz\\{\n  const FOO,\n};\n\nfunction a() {}\n")
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
	if got := root.EndByte(); got != uint32(len(src)) {
		t.Fatalf("root end = %d, want %d; tree=%s", got, len(src), root.SExpr(PhpLanguage()))
	}
	if got := root.ChildCount(); got != 5 {
		t.Fatalf("root child count = %d, want 5; tree=%s", got, root.SExpr(PhpLanguage()))
	}
	if fn := root.Child(4); fn == nil || fn.Type(PhpLanguage()) != "function_definition" {
		t.Fatalf("last child = %v, want function_definition; tree=%s", fn, root.SExpr(PhpLanguage()))
	}
}

func TestPHPTopLevelStaticAnonymousFunctionRecovery(t *testing.T) {
	src := []byte("<?php\nstatic function () {}\n")
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
	if got := root.EndByte(); got != uint32(len(src)) {
		t.Fatalf("root end = %d, want %d; tree=%s", got, len(src), root.SExpr(PhpLanguage()))
	}
	if got := root.ChildCount(); got != 2 {
		t.Fatalf("root child count = %d, want 2; tree=%s", got, root.SExpr(PhpLanguage()))
	}
	stmt := root.Child(1)
	if stmt == nil || stmt.Type(PhpLanguage()) != "expression_statement" {
		t.Fatalf("second child = %v, want expression_statement; tree=%s", stmt, root.SExpr(PhpLanguage()))
	}
	if got := stmt.ChildCount(); got != 2 {
		t.Fatalf("expression_statement child count = %d, want 2; tree=%s", got, root.SExpr(PhpLanguage()))
	}
	if fn := stmt.Child(0); fn == nil || fn.Type(PhpLanguage()) != "anonymous_function" {
		t.Fatalf("first expression child = %v, want anonymous_function; tree=%s", fn, root.SExpr(PhpLanguage()))
	}
	if semi := stmt.Child(1); semi == nil || semi.Type(PhpLanguage()) != ";" || !semi.HasError() {
		t.Fatalf("second expression child = %v, want missing semicolon; tree=%s", semi, root.SExpr(PhpLanguage()))
	}
}

// TestPHPListLiteralDestructuringTargetsMatchC pins the parity fix for
// destructuring targets. tree-sitter-c only accepts `_variable | list_literal`
// on the assignment LHS and in the foreach value position, so an array literal
// there is parsed as list_literal with bare children (no
// array_element_initializer). Go's GLR previously kept
// array_creation_expression/array_element_initializer for key=>value and
// pair-valued foreach forms; assert the C-faithful shape, and that genuine
// array literals (RHS positions) stay array_creation_expression.
func TestPHPListLiteralDestructuringTargetsMatchC(t *testing.T) {
	lang := PhpLanguage()
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "foreach_value_keyed",
			src:  "<?php foreach ($x as ['a' => $v]) {}",
			want: "(program (php_tag) (foreach_statement (variable_name (name)) (list_literal (string (string_content)) (variable_name (name))) (compound_statement)))",
		},
		{
			name: "foreach_pair_value_list",
			src:  "<?php foreach ($x as $k => [$a, $b]) {}",
			want: "(program (php_tag) (foreach_statement (variable_name (name)) (pair (variable_name (name)) (list_literal (variable_name (name)) (variable_name (name)))) (compound_statement)))",
		},
		{
			name: "assignment_keyed_destructure",
			src:  "<?php ['a' => $x, 'b' => $y] = $arr;",
			want: "(program (php_tag) (expression_statement (assignment_expression (list_literal (string (string_content)) (variable_name (name)) (string (string_content)) (variable_name (name))) (variable_name (name)))))",
		},
		{
			name: "rhs_array_literal_unchanged",
			src:  "<?php $arr = ['a' => 1, 'b' => 2];",
			want: "(program (php_tag) (expression_statement (assignment_expression (variable_name (name)) (array_creation_expression (array_element_initializer (string (string_content)) (integer)) (array_element_initializer (string (string_content)) (integer))))))",
		},
		{
			name: "mixed_lhs_list_rhs_array",
			src:  "<?php [$a, $b] = [$c, $d];",
			want: "(program (php_tag) (expression_statement (assignment_expression (list_literal (variable_name (name)) (variable_name (name))) (array_creation_expression (array_element_initializer (variable_name (name))) (array_element_initializer (variable_name (name)))))))",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parser := ts.NewParser(lang)
			tree, err := parser.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root == nil {
				t.Fatal("missing root node")
			}
			if got := root.SExpr(lang); got != tc.want {
				t.Fatalf("sexpr mismatch\n got=%s\nwant=%s", got, tc.want)
			}
		})
	}
}

func TestPHPTopLevelStaticNamedFunctionRecovery(t *testing.T) {
	src := []byte("<?php\nstatic function a() {}\n")
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
	if got := root.EndByte(); got != uint32(len(src)) {
		t.Fatalf("root end = %d, want %d; tree=%s", got, len(src), root.SExpr(PhpLanguage()))
	}
	if got := root.ChildCount(); got != 3 {
		t.Fatalf("root child count = %d, want 3; tree=%s", got, root.SExpr(PhpLanguage()))
	}
	if errNode := root.Child(1); errNode == nil || errNode.Type(PhpLanguage()) != "ERROR" {
		t.Fatalf("second child = %v, want ERROR; tree=%s", errNode, root.SExpr(PhpLanguage()))
	} else {
		staticModifier := findFirstPHPNodeOfType(errNode, PhpLanguage(), "static_modifier")
		if staticModifier == nil {
			t.Fatalf("missing static_modifier under ERROR; tree=%s", root.SExpr(PhpLanguage()))
		}
		if got := staticModifier.ChildCount(); got != 1 {
			t.Fatalf("static_modifier child count = %d, want 1; tree=%s", got, root.SExpr(PhpLanguage()))
		}
		if child := staticModifier.Child(0); child == nil || child.Type(PhpLanguage()) != "static" {
			t.Fatalf("static_modifier child = %v, want static; tree=%s", child, root.SExpr(PhpLanguage()))
		}
	}
	if body := root.Child(2); body == nil || body.Type(PhpLanguage()) != "compound_statement" {
		t.Fatalf("third child = %v, want compound_statement; tree=%s", body, root.SExpr(PhpLanguage()))
	}
}

func TestPHPContextStaticNamedFunctionRecovery(t *testing.T) {
	src := []byte("<?php\nfunction a() {}\n// <- @keyword\n\nstatic function a() {}\n")
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
	if got := root.EndByte(); got != uint32(len(src)) {
		t.Fatalf("root end = %d, want %d; tree=%s", got, len(src), root.SExpr(PhpLanguage()))
	}
	if got := root.ChildCount(); got != 6 {
		t.Fatalf("root child count = %d, want 6; tree=%s", got, root.SExpr(PhpLanguage()))
	}
	if errNode := root.Child(3); errNode == nil || errNode.Type(PhpLanguage()) != "ERROR" {
		t.Fatalf("child[3] = %v, want ERROR; tree=%s", errNode, root.SExpr(PhpLanguage()))
	}
	if stmt := root.Child(4); stmt == nil || stmt.Type(PhpLanguage()) != "expression_statement" {
		t.Fatalf("child[4] = %v, want expression_statement; tree=%s", stmt, root.SExpr(PhpLanguage()))
	}
	if body := root.Child(5); body == nil || body.Type(PhpLanguage()) != "compound_statement" {
		t.Fatalf("child[5] = %v, want compound_statement; tree=%s", body, root.SExpr(PhpLanguage()))
	}
}

func TestPHPTopLevelStaticNamedFunctionFollowedByArrowAndClassRecovery(t *testing.T) {
	src := []byte("<?php\nstatic function a() {}\nstatic fn () => 1;\nabstract class A {}\n")
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
	if got := root.EndByte(); got != uint32(len(src)) {
		t.Fatalf("root end = %d, want %d; tree=%s", got, len(src), root.SExpr(PhpLanguage()))
	}
	if got := root.ChildCount(); got != 5 {
		t.Fatalf("root child count = %d, want 5; tree=%s", got, root.SExpr(PhpLanguage()))
	}
	if errNode := root.Child(1); errNode == nil || errNode.Type(PhpLanguage()) != "ERROR" {
		t.Fatalf("child[1] = %v, want ERROR; tree=%s", errNode, root.SExpr(PhpLanguage()))
	}
	if body := root.Child(2); body == nil || body.Type(PhpLanguage()) != "compound_statement" {
		t.Fatalf("child[2] = %v, want compound_statement; tree=%s", body, root.SExpr(PhpLanguage()))
	}
	if stmt := root.Child(3); stmt == nil || stmt.Type(PhpLanguage()) != "expression_statement" {
		t.Fatalf("child[3] = %v, want expression_statement; tree=%s", stmt, root.SExpr(PhpLanguage()))
	}
	if decl := root.Child(4); decl == nil || decl.Type(PhpLanguage()) != "class_declaration" {
		t.Fatalf("child[4] = %v, want class_declaration; tree=%s", decl, root.SExpr(PhpLanguage()))
	}
}

func TestPHPModifierWrappersRestoreAnonymousChildren(t *testing.T) {
	src := []byte("<?php\nabstract class A {\n  private const BAR = 1;\n  protected readonly static $a;\n  final public $b;\n  public static function foo(): static {}\n}\n")
	parser := ts.NewParser(PhpLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	for parent, child := range map[string]string{
		"abstract_modifier":   "abstract",
		"final_modifier":      "final",
		"readonly_modifier":   "readonly",
		"static_modifier":     "static",
		"visibility_modifier": "private",
	} {
		assertFirstPHPWrapperChild(t, root, PhpLanguage(), parent, child)
	}
}

func findFirstPHPNodeOfType(node *ts.Node, lang *ts.Language, typ string) *ts.Node {
	if node == nil {
		return nil
	}
	if node.Type(lang) == typ {
		return node
	}
	for i := 0; i < node.ChildCount(); i++ {
		if found := findFirstPHPNodeOfType(node.Child(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}

func assertFirstPHPWrapperChild(t *testing.T, root *ts.Node, lang *ts.Language, parentType, childType string) {
	t.Helper()
	parent := findFirstPHPNodeOfType(root, lang, parentType)
	if parent == nil {
		t.Fatalf("missing %s; tree=%s", parentType, root.SExpr(lang))
	}
	if got := parent.ChildCount(); got != 1 {
		t.Fatalf("%s child count = %d, want 1; tree=%s", parentType, got, root.SExpr(lang))
	}
	if child := parent.Child(0); child == nil || child.Type(lang) != childType {
		t.Fatalf("%s child = %v, want %s; tree=%s", parentType, child, childType, root.SExpr(lang))
	}
}
