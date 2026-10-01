//go:build !grammar_subset || grammar_subset_kotlin

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestKotlinDottedPackageAndImportsParseWithoutErrors(t *testing.T) {
	src := []byte("package a.b.c\n\nimport d.y.*\nimport x.y.Z\nfun main() {}\n")
	lang := KotlinLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	if root.HasError() {
		t.Fatalf("Kotlin dotted package/import parse has errors:\n%s", root.SExpr(lang))
	}
}

func TestKotlinFunInterfaceWithPackageImportParsesWithoutErrors(t *testing.T) {
	src := []byte("package com.example\n\nimport com.example.dep.Foo\n\nfun interface MyHandler {\n    fun handle(value: String): Boolean\n}\n")
	lang := KotlinLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	if root.HasError() {
		t.Fatalf("Kotlin fun interface parse has errors:\n%s", root.SExpr(lang))
	}
}

func TestKotlinModifierWrappersRestoreAnonymousChildren(t *testing.T) {
	lang := KotlinLanguage()
	parser := gotreesitter.NewParser(lang)
	src := []byte(`
package com.example

private data class Box<out T>(private val value: T) {
  private val enabled = true
  public inline fun <reified R> expose(vararg items: R): String = this.toString() + super.toString() + value.toString()
}
`)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	if root.HasError() {
		t.Fatalf("Kotlin modifier parse has errors:\n%s", root.SExpr(lang))
	}

	assertFirstKotlinWrapperChild(t, root, lang, "visibility_modifier", "private")
	assertFirstKotlinWrapperChild(t, root, lang, "class_modifier", "data")
	assertFirstKotlinWrapperChild(t, root, lang, "variance_modifier", "out")
	assertFirstKotlinWrapperChild(t, root, lang, "function_modifier", "inline")
	assertFirstKotlinWrapperChild(t, root, lang, "reification_modifier", "reified")
	assertFirstKotlinWrapperChild(t, root, lang, "parameter_modifier", "vararg")
	assertFirstKotlinTextWrapperChild(t, root, lang, src, "simple_identifier", "value", "value")
	assertFirstKotlinWrapperChild(t, root, lang, "boolean_literal", "true")
	assertFirstKotlinWrapperChild(t, root, lang, "this_expression", "this")
	assertFirstKotlinWrapperChild(t, root, lang, "super_expression", "super")
}

func findFirstKotlinNodeOfType(node *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if node.Type(lang) == typ {
		return node
	}
	for i := 0; i < node.ChildCount(); i++ {
		if found := findFirstKotlinNodeOfType(node.Child(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}

func assertFirstKotlinWrapperChild(t *testing.T, root *gotreesitter.Node, lang *gotreesitter.Language, parentType, childType string) {
	t.Helper()
	parent := findFirstKotlinNodeOfType(root, lang, parentType)
	if parent == nil {
		t.Fatalf("missing %s; tree=%s", parentType, root.SExpr(lang))
	}
	if got := parent.ChildCount(); got != 1 {
		t.Fatalf("%s child count = %d, want 1; tree=%s", parentType, got, root.SExpr(lang))
	}
	child := parent.Child(0)
	if child == nil {
		t.Fatalf("%s child is nil; tree=%s", parentType, root.SExpr(lang))
	}
	if child.Type(lang) != childType || child.IsNamed() {
		t.Fatalf("%s child type/named = %q/%v, want %s/false; tree=%s", parentType, child.Type(lang), child.IsNamed(), childType, root.SExpr(lang))
	}
}

func assertFirstKotlinTextWrapperChild(t *testing.T, root *gotreesitter.Node, lang *gotreesitter.Language, source []byte, parentType, parentText, childType string) {
	t.Helper()
	parent := findFirstKotlinNode(root, lang, func(n *gotreesitter.Node) bool {
		return n.Type(lang) == parentType && n.Text(source) == parentText
	})
	if parent == nil {
		t.Fatalf("missing %s text %q; tree=%s", parentType, parentText, root.SExpr(lang))
	}
	if got := parent.ChildCount(); got != 1 {
		t.Fatalf("%s %q child count = %d, want 1; tree=%s", parentType, parentText, got, root.SExpr(lang))
	}
	child := parent.Child(0)
	if child == nil {
		t.Fatalf("%s %q child is nil; tree=%s", parentType, parentText, root.SExpr(lang))
	}
	if child.Type(lang) != childType || child.IsNamed() {
		t.Fatalf("%s %q child type/named = %q/%v, want %s/false; tree=%s", parentType, parentText, child.Type(lang), child.IsNamed(), childType, root.SExpr(lang))
	}
}

func findFirstKotlinNode(node *gotreesitter.Node, lang *gotreesitter.Language, match func(*gotreesitter.Node) bool) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if match != nil && match(node) {
		return node
	}
	for i := 0; i < node.ChildCount(); i++ {
		if found := findFirstKotlinNode(node.Child(i), lang, match); found != nil {
			return found
		}
	}
	return nil
}
