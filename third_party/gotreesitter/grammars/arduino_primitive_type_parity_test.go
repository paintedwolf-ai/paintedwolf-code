package grammars

import (
	"strings"
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestArduinoVoidParameterListPrimitiveType verifies that the `void` keyword in
// an explicit empty parameter list `(void)` is tagged as a primitive_type, the
// same shape tree-sitter-c (which Arduino extends) produces. Previously the
// parser tagged it as a user type_identifier because the C builtin-primitive
// promotion pass only ran for the "c"/"cpp" languages, never for "arduino".
func TestArduinoVoidParameterListPrimitiveType(t *testing.T) {
	src := []byte("unsigned long eeprom_crc(void) {\n  return 0;\n}\n")
	parser := ts.NewParser(ArduinoLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if root.HasError() {
		t.Fatalf("unexpected parse errors: %s", root.SExpr(ArduinoLanguage()))
	}

	var found *ts.Node
	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		if n == nil {
			return
		}
		if n.Type(ArduinoLanguage()) == "parameter_declaration" {
			if c := n.Child(0); c != nil && c.Text(src) == "void" {
				found = c
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)

	if found == nil {
		t.Fatalf("did not find (void) parameter declaration; tree=%s", root.SExpr(ArduinoLanguage()))
	}
	if got := found.Type(ArduinoLanguage()); got != "primitive_type" {
		t.Fatalf("(void) parameter: got %q, want primitive_type (matches tree-sitter-c); tree=%s",
			got, root.SExpr(ArduinoLanguage()))
	}
}

// TestArduinoBuiltinPrimitiveTypesPromoted is a broader guard covering several
// builtin C primitive keywords that the Arduino grammar inherits. Each must be a
// primitive_type, never a type_identifier.
func TestArduinoBuiltinPrimitiveTypesPromoted(t *testing.T) {
	src := []byte("int f(void) {\n  char c = 0;\n  return c;\n}\n")
	parser := ts.NewParser(ArduinoLanguage())
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("unexpected parse errors: %s", root.SExpr(ArduinoLanguage()))
	}
	sexpr := root.SExpr(ArduinoLanguage())
	for _, kw := range []string{"void", "int", "char"} {
		// A builtin keyword should never survive as a user type_identifier.
		if strings.Contains(sexpr, "(type_identifier)") {
			// Inspect leaves directly to give a precise message.
			var bad string
			var walk func(n *ts.Node)
			walk = func(n *ts.Node) {
				if n == nil {
					return
				}
				if n.Type(ArduinoLanguage()) == "type_identifier" {
					switch n.Text(src) {
					case "void", "int", "char":
						bad = n.Text(src)
					}
				}
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
			}
			walk(root)
			if bad != "" {
				t.Fatalf("builtin primitive %q left as type_identifier; tree=%s", bad, sexpr)
			}
		}
		_ = kw
	}
}
