package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func bashMustParseNoError(t *testing.T, src []byte) (*gotreesitter.Tree, *gotreesitter.Language) {
	t.Helper()

	lang := BashLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	if root.HasError() {
		t.Fatalf("expected error-free Bash parse tree, got %s", root.SExpr(lang))
	}
	return tree, lang
}

func TestBashLanguageRegistersExternalLexStates(t *testing.T) {
	lang := BashLanguage()
	if got := len(lang.ExternalLexStates); got == 0 {
		t.Fatal("BashLanguage.ExternalLexStates is empty")
	}
}

func TestBashIfSubshellParsesWithoutError(t *testing.T) {
	src := []byte(`if [ $ret -eq 0 ]; then
  (exit 0)
else
  rm npm-install-$$.sh
  echo "Failed to download script" >&2
  exit $ret
fi
`)

	tree, lang := bashMustParseNoError(t, src)
	root := tree.RootNode()

	var subshell *gotreesitter.Node
	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if node.IsNamed() && node.Type(lang) == "subshell" {
			subshell = node
			return gotreesitter.WalkStop
		}
		return gotreesitter.WalkContinue
	})
	if subshell == nil {
		t.Fatalf("expected subshell node in Bash parse tree: %s", root.SExpr(lang))
	}
	if got, want := subshell.Text(src), "(exit 0)"; got != want {
		t.Fatalf("subshell text = %q, want %q", got, want)
	}
}

func TestBashRepeatedRedirectsKeepRedirectField(t *testing.T) {
	src := []byte("which $readlink >/dev/null 2>/dev/null\n")

	tree, lang := bashMustParseNoError(t, src)
	root := tree.RootNode()
	stmt := root.NamedChild(0)
	if stmt == nil {
		t.Fatalf("expected redirected_statement, got %s", root.SExpr(lang))
	}
	if got, want := stmt.Type(lang), "redirected_statement"; got != want {
		t.Fatalf("statement type = %q, want %q", got, want)
	}
	if got, want := stmt.ChildCount(), 3; got != want {
		t.Fatalf("redirected_statement child count = %d, want %d", got, want)
	}
	for _, idx := range []int{1, 2} {
		if got, want := stmt.FieldNameForChild(idx, lang), "redirect"; got != want {
			t.Fatalf("child %d field = %q, want %q in %s", idx, got, want, root.SExpr(lang))
		}
	}
}

func TestBashCaseHeaderKeepsInKeywordSeparate(t *testing.T) {
	src := []byte(`case $node_version in
  *)
    echo x >&2
    ;;
esac
`)

	tree, lang := bashMustParseNoError(t, src)
	root := tree.RootNode()
	stmt := root.NamedChild(0)
	if stmt == nil {
		t.Fatalf("expected case_statement, got %s", root.SExpr(lang))
	}
	if got, want := stmt.Type(lang), "case_statement"; got != want {
		t.Fatalf("statement type = %q, want %q in %s", got, want, root.SExpr(lang))
	}

	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if node.Type(lang) == "heredoc_redirect_token1" {
			t.Fatalf("unexpected heredoc_redirect_token1 in Bash case tree: %s", root.SExpr(lang))
		}
		return gotreesitter.WalkContinue
	})
}
