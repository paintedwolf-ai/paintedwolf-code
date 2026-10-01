package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestJavaGLRRuntime(t *testing.T) {
	lang := DetectLanguage("Test.java").Language()
	parser := gotreesitter.NewParser(lang)
	parser.SetGLRTrace(true)

	src := []byte(`public class Foo { void f() { obj.method(); } }`)
	tree, _ := parser.Parse(src)
	root := tree.RootNode()
	t.Logf("hasError=%v  sexpr=%s", root.HasError(), root.SExpr(lang))
}
