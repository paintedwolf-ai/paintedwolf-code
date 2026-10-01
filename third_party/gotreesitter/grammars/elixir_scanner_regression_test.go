package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestElixirBitstringAfterBlankLineRegression(t *testing.T) {
	lang := ElixirLanguage()
	parser := gotreesitter.NewParser(lang)

	src := []byte("<<1, 2, 3>>\n\n<< header :: size(8), data :: binary >>\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("unexpected error tree: %s", root.SExpr(lang))
	}
	if got, want := root.NamedChildCount(), 2; got != want {
		t.Fatalf("root named child count = %d, want %d: %s", got, want, root.SExpr(lang))
	}
	if got := root.NamedChild(0).Type(lang); got != "bitstring" {
		t.Fatalf("first named child type = %q, want bitstring: %s", got, root.SExpr(lang))
	}
	if got := root.NamedChild(1).Type(lang); got != "bitstring" {
		t.Fatalf("second named child type = %q, want bitstring: %s", got, root.SExpr(lang))
	}
}

func TestElixirMultipleModuledocBeforeHeredocRegression(t *testing.T) {
	lang := ElixirLanguage()
	parser := gotreesitter.NewParser(lang)

	src := []byte("defmodule M do\n  @moduledoc \"Simple doc\"\n\n  @moduledoc false\n\n  @moduledoc \"\"\"\n  Heredoc doc\n  \"\"\"\nend\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("unexpected error tree: %s", root.SExpr(lang))
	}
	if got, want := root.NamedChildCount(), 1; got != want {
		t.Fatalf("root named child count = %d, want %d: %s", got, want, root.SExpr(lang))
	}
	if got := root.NamedChild(0).Type(lang); got != "call" {
		t.Fatalf("root child type = %q, want call: %s", got, root.SExpr(lang))
	}
}

func TestElixirNestedCallTargetFieldRegression(t *testing.T) {
	lang := ElixirLanguage()
	parser := gotreesitter.NewParser(lang)

	src := []byte("def unquote(f)(x), do: nil\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("unexpected error tree: %s", root.SExpr(lang))
	}
	defCall := root.NamedChild(0)
	if defCall == nil || defCall.Type(lang) != "call" {
		t.Fatalf("root child type = %q, want call: %s", defCall.Type(lang), root.SExpr(lang))
	}
	args := defCall.Child(1)
	if args == nil || args.Type(lang) != "arguments" {
		t.Fatalf("def args type = %q, want arguments: %s", args.Type(lang), root.SExpr(lang))
	}
	nested := args.NamedChild(0)
	if nested == nil || nested.Type(lang) != "call" {
		t.Fatalf("nested child type = %q, want call: %s", nested.Type(lang), root.SExpr(lang))
	}
	if got := nested.FieldNameForChild(0, lang); got != "target" {
		t.Fatalf("nested call child field = %q, want target: %s", got, root.SExpr(lang))
	}
}

func TestElixirLiteralsRestoreAnonymousKeywordChild(t *testing.T) {
	lang := ElixirLanguage()
	parser := gotreesitter.NewParser(lang)

	src := []byte("false\ntrue\nnil\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("unexpected error tree: %s", root.SExpr(lang))
	}
	booleans := elixirNamedDescendants(root, lang, "boolean")
	if got, want := len(booleans), 2; got != want {
		t.Fatalf("boolean count = %d, want %d: %s", got, want, root.SExpr(lang))
	}
	for i, want := range []string{"false", "true"} {
		n := booleans[i]
		if got := n.ChildCount(); got != 1 {
			t.Fatalf("boolean[%d] child count = %d, want 1: %s", i, got, root.SExpr(lang))
		}
		child := n.Child(0)
		if child == nil || child.Type(lang) != want {
			t.Fatalf("boolean[%d] child = %v, want %q: %s", i, child, want, root.SExpr(lang))
		}
		if child.IsNamed() {
			t.Fatalf("boolean[%d] child should be anonymous: %s", i, root.SExpr(lang))
		}
	}
	nils := elixirNamedDescendants(root, lang, "nil")
	if got, want := len(nils), 1; got != want {
		t.Fatalf("nil count = %d, want %d: %s", got, want, root.SExpr(lang))
	}
	nilNode := nils[0]
	if got := nilNode.ChildCount(); got != 1 {
		t.Fatalf("nil child count = %d, want 1: %s", got, root.SExpr(lang))
	}
	child := nilNode.Child(0)
	if child == nil || child.Type(lang) != "nil" {
		t.Fatalf("nil child = %v, want anonymous nil: %s", child, root.SExpr(lang))
	}
	if child.IsNamed() {
		t.Fatalf("nil child should be anonymous: %s", root.SExpr(lang))
	}
}

func TestElixirKeywordMapContentCompatibilityRegression(t *testing.T) {
	lang := ElixirLanguage()
	parser := gotreesitter.NewParser(lang)

	src := []byte("%{shortcut: \"syntax\"}\n%{map | name: \"Silly\"}\n%{\"content-type\" => \"te\" <> \"xt\"}\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("unexpected error tree: %s", root.SExpr(lang))
	}
	maps := elixirNamedDescendants(root, lang, "map")
	if got, want := len(maps), 3; got != want {
		t.Fatalf("map count = %d, want %d: %s", got, want, root.SExpr(lang))
	}

	content := elixirFirstNamedDescendant(maps[0], lang, "map_content")
	if content == nil {
		t.Fatalf("first map missing map_content: %s", maps[0].SExpr(lang))
	}
	keywords := content.Child(0)
	if keywords == nil || keywords.Type(lang) != "keywords" {
		t.Fatalf("first map_content child = %v, want keywords: %s", keywords, content.SExpr(lang))
	}
	if pair := keywords.Child(0); pair == nil || pair.Type(lang) != "pair" {
		t.Fatalf("keywords child = %v, want pair: %s", pair, keywords.SExpr(lang))
	}

	updateContent := elixirFirstNamedDescendant(maps[1], lang, "map_content")
	if updateContent == nil {
		t.Fatalf("second map missing map_content: %s", maps[1].SExpr(lang))
	}
	binary := updateContent.Child(0)
	if binary == nil || binary.Type(lang) != "binary_operator" {
		t.Fatalf("second map_content child = %v, want binary_operator: %s", binary, updateContent.SExpr(lang))
	}
	if got := updateContent.FieldNameForChild(0, lang); got != "" {
		t.Fatalf("second map_content child field = %q, want empty: %s", got, updateContent.SExpr(lang))
	}
	if got, want := binary.ChildCount(), 3; got != want {
		t.Fatalf("binary_operator child count = %d, want %d: %s", got, want, binary.SExpr(lang))
	}
	for i, want := range []string{"left", "operator", "right"} {
		if got := binary.FieldNameForChild(i, lang); got != want {
			t.Fatalf("binary_operator child[%d] field = %q, want %q: %s", i, got, want, binary.SExpr(lang))
		}
	}
	keywords = binary.Child(2)
	if keywords == nil || keywords.Type(lang) != "keywords" {
		t.Fatalf("binary_operator child[2] = %v, want keywords: %s", keywords, binary.SExpr(lang))
	}

	fatArrowContent := elixirFirstNamedDescendant(maps[2], lang, "map_content")
	if fatArrowContent == nil {
		t.Fatalf("third map missing map_content: %s", maps[2].SExpr(lang))
	}
	binary = fatArrowContent.Child(0)
	if binary == nil || binary.Type(lang) != "binary_operator" {
		t.Fatalf("third map_content child = %v, want binary_operator: %s", binary, fatArrowContent.SExpr(lang))
	}
	if got := fatArrowContent.FieldNameForChild(0, lang); got != "" {
		t.Fatalf("third map_content child field = %q, want empty: %s", got, fatArrowContent.SExpr(lang))
	}
	if op := binary.Child(1); op == nil || op.Type(lang) != "=>" {
		t.Fatalf("third binary_operator child[1] = %v, want =>: %s", op, binary.SExpr(lang))
	}
	for i, want := range []string{"left", "operator", "right"} {
		if got := binary.FieldNameForChild(i, lang); got != want {
			t.Fatalf("third binary_operator child[%d] field = %q, want %q: %s", i, got, want, binary.SExpr(lang))
		}
	}
}

func elixirFirstNamedDescendant(node *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if node.IsNamed() && node.Type(lang) == typ {
		return node
	}
	for i := 0; i < node.NamedChildCount(); i++ {
		if found := elixirFirstNamedDescendant(node.NamedChild(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}

func elixirNamedDescendants(node *gotreesitter.Node, lang *gotreesitter.Language, typ string) []*gotreesitter.Node {
	if node == nil {
		return nil
	}
	var out []*gotreesitter.Node
	if node.IsNamed() && node.Type(lang) == typ {
		out = append(out, node)
	}
	for i := 0; i < node.NamedChildCount(); i++ {
		out = append(out, elixirNamedDescendants(node.NamedChild(i), lang, typ)...)
	}
	return out
}
