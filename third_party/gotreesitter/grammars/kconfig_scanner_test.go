//go:build !grammar_subset || grammar_subset_kconfig

package grammars

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestKconfigHelpTextKeepsApostrophesInIndentedBlock(t *testing.T) {
	src := []byte("config FOO\n" +
		"\tbool \"Foo\"\n" +
		"\thelp\n" +
		"\t  First help line stays text.\n" +
		"\t  zero'ed bits should not start a string.\n" +
		"\t  won't split the help text either.\n" +
		"\n" +
		"\t  Blank lines with matching indentation stay in the block.\n" +
		"config BAR\n" +
		"\tbool \"Bar\"\n")

	lang := KconfigLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		t.Fatal("Parse() returned nil root")
	}
	root := tree.RootNode()
	if root.Type(lang) == "ERROR" {
		t.Fatalf("root type = ERROR:\n%s", root.SExpr(lang))
	}
	if root.HasError() {
		t.Fatalf("root.HasError = true:\n%s", root.SExpr(lang))
	}

	help := firstKconfigNodeByType(root, lang, "help_text")
	if help == nil {
		t.Fatalf("missing help_text node:\n%s", root.SExpr(lang))
	}
	helpText := help.Text(src)
	for _, want := range []string{
		"First help line stays text.",
		"zero'ed bits should not start a string.",
		"won't split the help text either.",
		"Blank lines with matching indentation stay in the block.",
	} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help_text missing %q; got %q", want, helpText)
		}
	}
	if strings.Contains(helpText, "config BAR") {
		t.Fatalf("help_text consumed dedented config: %q", helpText)
	}
}

func TestKconfigHelpTextAtEOF(t *testing.T) {
	src := []byte("config FOO\n" +
		"\tbool \"Foo\"\n" +
		"\thelp\n" +
		"\t  EOF help line with zero'ed bits.\n" +
		"\t  EOF help line that won't terminate early.")

	lang := KconfigLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		t.Fatal("Parse() returned nil root")
	}
	root := tree.RootNode()
	if root.Type(lang) == "ERROR" {
		t.Fatalf("root type = ERROR:\n%s", root.SExpr(lang))
	}
	if root.HasError() {
		t.Fatalf("root.HasError = true:\n%s", root.SExpr(lang))
	}

	help := firstKconfigNodeByType(root, lang, "help_text")
	if help == nil {
		t.Fatalf("missing help_text node:\n%s", root.SExpr(lang))
	}
	helpText := help.Text(src)
	for _, want := range []string{
		"EOF help line with zero'ed bits.",
		"EOF help line that won't terminate early.",
	} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help_text missing %q; got %q", want, helpText)
		}
	}
}

func firstKconfigNodeByType(n *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	if n == nil {
		return nil
	}
	if n.Type(lang) == typ {
		return n
	}
	for i := 0; i < n.ChildCount(); i++ {
		if found := firstKconfigNodeByType(n.Child(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}
