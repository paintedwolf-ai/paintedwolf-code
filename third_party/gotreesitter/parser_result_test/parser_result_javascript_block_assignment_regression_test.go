package parserresult_test

import (
	"strings"
	"testing"
)

func TestJavaScriptStandaloneBlockBeforeSimpleAssignment(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "simple assignment", src: "{a}b=c"},
		{name: "compound assignment", src: "{a}b+=c"},
		{name: "explicit semicolon", src: "{a};b=c"},
		{name: "call expression", src: "{a}b()"},
		{name: "identifier expression", src: "{a}b"},
		{name: "if block assignment", src: "if(x){}y=z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree, lang := parseByLanguageName(t, "javascript", tc.src)
			root := tree.RootNode()
			if got := root.Type(lang); got != "program" {
				t.Fatalf("root type = %q, want program: %s", got, root.SExpr(lang))
			}
			if root.HasError() {
				t.Fatalf("unexpected javascript parse error: %s", root.SExpr(lang))
			}
			if tc.src == "{a}b=c" {
				sexpr := root.SExpr(lang)
				if !strings.Contains(sexpr, "(statement_block") {
					t.Fatalf("standalone block missing statement_block: %s", sexpr)
				}
				if !strings.Contains(sexpr, "(assignment_expression") {
					t.Fatalf("assignment tail missing assignment_expression: %s", sexpr)
				}
			}
		})
	}
}

func TestJavaScriptAutomaticSemicolonPrecedesTrailingComment(t *testing.T) {
	src := "function f() {\n  if (!size || !pre) { i--; break; } // counter\n  var curr = 1;\n}\n"
	tree, lang := parseByLanguageName(t, "javascript", src)
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("unexpected javascript parse error: %s", root.SExpr(lang))
	}

	function := root.Child(0)
	if function == nil || function.ChildCount() != 4 {
		t.Fatalf("function shape mismatch: %s", root.SExpr(lang))
	}
	outer := function.Child(3)
	if outer == nil || outer.Type(lang) != "statement_block" || outer.ChildCount() != 5 {
		t.Fatalf("outer statement block mismatch: %s", root.SExpr(lang))
	}
	wantTypes := []string{"{", "if_statement", "comment", "variable_declaration", "}"}
	for i, want := range wantTypes {
		if got := outer.Child(i).Type(lang); got != want {
			t.Fatalf("outer child %d type = %q, want %q: %s", i, got, want, root.SExpr(lang))
		}
	}
	comment := outer.Child(2)
	if !comment.IsExtra() || comment.StartByte() != 52 || comment.EndByte() != 62 {
		t.Fatalf("outer comment range/flags = [%d:%d] extra=%v, want [52:62] extra: %s",
			comment.StartByte(), comment.EndByte(), comment.IsExtra(), root.SExpr(lang))
	}
	ifStatement := outer.Child(1)
	consequence := ifStatement.Child(2)
	if consequence == nil || consequence.Type(lang) != "statement_block" || consequence.EndByte() != 51 {
		t.Fatalf("if consequence must end at closing brace byte 51: %s", root.SExpr(lang))
	}
	for i := 0; i < consequence.ChildCount(); i++ {
		if consequence.Child(i).Type(lang) == "comment" {
			t.Fatalf("trailing comment remained inside if consequence: %s", root.SExpr(lang))
		}
	}
}

func TestJavaScriptJSXAttributeAfterExpressionStillParses(t *testing.T) {
	tests := []string{
		"<A a={b} c={d} />",
		"<A>{b}</A>",
	}

	for _, src := range tests {
		t.Run(src, func(t *testing.T) {
			tree, lang := parseByLanguageName(t, "javascript", src)
			root := tree.RootNode()
			if got := root.Type(lang); got != "program" {
				t.Fatalf("root type = %q, want program: %s", got, root.SExpr(lang))
			}
			if root.HasError() {
				t.Fatalf("unexpected javascript JSX parse error: %s", root.SExpr(lang))
			}
		})
	}
}

func TestTypeScriptStandaloneBlockBeforeSimpleAssignment(t *testing.T) {
	for _, langName := range []string{"typescript", "tsx"} {
		t.Run(langName, func(t *testing.T) {
			tree, lang := parseByLanguageName(t, langName, "{a}b=c")
			root := tree.RootNode()
			if got := root.Type(lang); got != "program" {
				t.Fatalf("%s root type = %q, want program: %s", langName, got, root.SExpr(lang))
			}
			if root.HasError() {
				t.Fatalf("unexpected %s parse error: %s", langName, root.SExpr(lang))
			}
		})
	}
}
