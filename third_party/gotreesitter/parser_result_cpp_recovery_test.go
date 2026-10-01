package gotreesitter_test

import (
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestCppMalformedClassFunctionDefinitionRecovery(t *testing.T) {
	src := []byte(`int main() {
  a<T>();
  // <- function

  a::b();
  // ^ function

  a::b<C, D>();
  // ^ function

  this->b<C, D>();
  //    ^ function

  auto x = y;
  // <- type

  vector<T> a;
  // <- type

  std::vector<T> a;
  //   ^ type
}

class C : D{
  A();
  // <- function

  void efg() {
    // ^ function
  }
}

void A::b() {
  //    ^ function
}
`)
	lang := grammars.CppLanguage()
	tree, err := gts.NewParser(lang).ParseWithTokenSource(src, grammars.NewCTokenSourceOrEOF(src, lang))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if got, want := root.Type(lang), "translation_unit"; got != want {
		t.Fatalf("root type = %q, want %q\n%s", got, want, root.SExpr(lang))
	}
	if !root.HasError() {
		t.Fatalf("root.HasError = false, want true")
	}
	// The C oracle folds the malformed class and following `void A::b() {}`
	// into one recovered function_definition. Keep the cpp compatibility
	// normalizer scoped to this C shape instead of enabling cpp C-recovery
	// globally; the latter regressed corpus agreement in earlier A/B runs.
	if got, want := root.ChildCount(), 2; got != want {
		t.Fatalf("root child count = %d, want %d\n%s", got, want, root.SExpr(lang))
	}
	recovered := root.Child(1)
	if got, want := recovered.Type(lang), "function_definition"; got != want {
		t.Fatalf("root.Child(1) = %q, want %q\n%s", got, want, root.SExpr(lang))
	}
	if !recovered.HasError() {
		t.Fatalf("recovered function_definition HasError = false, want true\n%s", recovered.SExpr(lang))
	}
	if got, want := recovered.StartByte(), uint32(234); got != want {
		t.Fatalf("recovered function start = %d, want %d", got, want)
	}
	if got, want := recovered.EndByte(), uint32(346); got != want {
		t.Fatalf("recovered function end = %d, want %d", got, want)
	}
	if got, want := recovered.ChildCount(), 3; got != want {
		t.Fatalf("recovered child count = %d, want %d\n%s", got, want, recovered.SExpr(lang))
	}
	if got, want := recovered.Child(0).Type(lang), "class_specifier"; got != want {
		t.Fatalf("recovered child[0] = %q, want %q\n%s", got, want, recovered.SExpr(lang))
	}
	declarator := recovered.Child(1)
	if got, want := declarator.Type(lang), "function_declarator"; got != want {
		t.Fatalf("recovered child[1] = %q, want %q\n%s", got, want, recovered.SExpr(lang))
	}
	qualified := declarator.Child(0)
	if got, want := qualified.Type(lang), "qualified_identifier"; got != want {
		t.Fatalf("declarator child[0] = %q, want %q\n%s", got, want, declarator.SExpr(lang))
	}
	if got, want := qualified.ChildCount(), 4; got != want {
		t.Fatalf("qualified_identifier child count = %d, want %d\n%s", got, want, qualified.SExpr(lang))
	}
	if got, want := qualified.Child(0).Type(lang), "namespace_identifier"; got != want {
		t.Fatalf("qualified child[0] = %q, want %q\n%s", got, want, qualified.SExpr(lang))
	}
	if got, want := qualified.Child(0).Text(src), "void"; got != want {
		t.Fatalf("qualified child[0] text = %q, want %q", got, want)
	}
	errNode := qualified.Child(1)
	if got, want := errNode.Type(lang), "ERROR"; got != want {
		t.Fatalf("qualified child[1] = %q, want %q\n%s", got, want, qualified.SExpr(lang))
	}
	if !errNode.HasError() || !errNode.IsExtra() {
		t.Fatalf("qualified ERROR flags extra=%v hasError=%v, want both true\n%s", errNode.IsExtra(), errNode.HasError(), qualified.SExpr(lang))
	}
	if got, want := errNode.Child(0).Type(lang), "identifier"; got != want {
		t.Fatalf("qualified ERROR child = %q, want %q\n%s", got, want, errNode.SExpr(lang))
	}
	if got, want := errNode.Child(0).Text(src), "A"; got != want {
		t.Fatalf("qualified ERROR child text = %q, want %q", got, want)
	}
}
