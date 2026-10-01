package grammargen

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestCMultilinePreprocessorSequenceParity(t *testing.T) {
	genLang, refLang := loadGeneratedCLanguage(t)
	src := []byte(`#define ONE
    #define TWO int a = b;
#define THREE \
  c == d ? \
  e : \
  f
#define FOUR (mno * pq)
#define FIVE(a,b) x \
                  + y
#define SIX(a,   \
            b) x \
               + y
#define SEVEN 7/* seven has an
                * annoying comment */
#define EIGHT(x) do { \
        x = x + 1;  \
        x = x / 2;  \
    } while (x > 0);
`)

	genTree, err := gotreesitter.NewParser(genLang).Parse(src)
	if err != nil {
		t.Fatalf("gen parse: %v", err)
	}
	defer genTree.Release()

	refTree, err := gotreesitter.NewParser(refLang).Parse(src)
	if err != nil {
		t.Fatalf("ref parse: %v", err)
	}
	defer refTree.Release()

	genRoot := genTree.RootNode()
	refRoot := refTree.RootNode()
	if genRoot == nil || refRoot == nil {
		t.Fatal("nil root")
	}
	if genRoot.HasError() {
		t.Fatalf("generated tree has ERROR: %s", genRoot.SExpr(genLang))
	}
	if refRoot.HasError() {
		t.Fatalf("reference tree has ERROR: %s", refRoot.SExpr(refLang))
	}

	if got, want := genRoot.EndByte(), refRoot.EndByte(); got != want {
		t.Fatalf("generated root end byte = %d, want %d\n  gen: %s\n  ref: %s", got, want, genRoot.SExpr(genLang), refRoot.SExpr(refLang))
	}

	if divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 10); len(divs) > 0 {
		t.Fatalf("deep parity mismatch: %v\n  gen: %s\n  ref: %s", divs, genRoot.SExpr(genLang), refRoot.SExpr(refLang))
	}
}

func TestCPreprocIfEscapedNewlineConditionParity(t *testing.T) {
	genLang, refLang := loadGeneratedCLanguage(t)
	src := []byte("#if A(B || C) && \\\n    !D(F)\n\nuint32_t a;\n\n#endif")

	genTree, err := gotreesitter.NewParser(genLang).Parse(src)
	if err != nil {
		t.Fatalf("gen parse: %v", err)
	}
	defer genTree.Release()

	refTree, err := gotreesitter.NewParser(refLang).Parse(src)
	if err != nil {
		t.Fatalf("ref parse: %v", err)
	}
	defer refTree.Release()

	genRoot := genTree.RootNode()
	refRoot := refTree.RootNode()
	if genRoot == nil || refRoot == nil {
		t.Fatal("nil root")
	}
	if genRoot.HasError() {
		t.Fatalf("generated tree has ERROR: %s", genRoot.SExpr(genLang))
	}
	if refRoot.HasError() {
		t.Fatalf("reference tree has ERROR: %s", refRoot.SExpr(refLang))
	}
	if got, want := genRoot.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("generated root end byte = %d, want %d\n  gen: %s", got, want, genRoot.SExpr(genLang))
	}
	if got, want := refRoot.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("reference root end byte = %d, want %d\n  ref: %s", got, want, refRoot.SExpr(refLang))
	}
	if divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 10); len(divs) > 0 {
		t.Fatalf("deep parity mismatch: %v\n  gen: %s\n  ref: %s", divs, genRoot.SExpr(genLang), refRoot.SExpr(refLang))
	}
}
