package grammargen

import (
	"fmt"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
)

func TestJavaSwitchDefaultLabelsDeepParity(t *testing.T) {
	spec, ok := importParityGrammarByName("java")
	if !ok {
		t.Fatal("missing java import parity grammar")
	}
	gram, err := importParityGrammarSource(spec)
	if err != nil {
		t.Skipf("Java grammar source unavailable: %v", err)
	}
	timeout := spec.genTimeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	genLang, err := generateWithTimeout(gram, timeout)
	if err != nil {
		t.Fatalf("generate Java: %v", err)
	}
	refLang := spec.blobFunc()
	adaptExternalScanner(refLang, genLang)

	for _, tc := range []struct {
		name string
		src  string
	}{
		{
			name: "traditional_switch_statement",
			src: `public class SwitchDemo {
  public static void main(String[] args) {
    int destinysChild = 2;
    String destinysChildString;
    switch (destinysChild) {
        case 1:  destinysChildString = "Beyonce";
                 break;
        case 2:  destinysChildString = "Kelly";
                 break;
        case 3:  destinysChildString = "Michelle";
                 break;
        default: destinysChildString = "Invalid";
                 break;
    }
    System.out.println(destinysChildString);
  }
}
`,
		},
		{
			name: "traditional_style_switch_expression",
			src: `class Test {
    int d = 3;
    static final int NUM = 2;
    void main() {
        int result = switch (d) {
            case 5 + 6:
                yield 1;
            case NUM:
                yield 2;
            default:
                System.out.println("hmmm...");
                yield 0;
        };
    }
}
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertJavaSwitchDefaultDeepParity(t, genLang, refLang, []byte(tc.src))
		})
	}
}

func assertJavaSwitchDefaultDeepParity(t *testing.T, genLang, refLang *gotreesitter.Language, src []byte) {
	t.Helper()

	genTree := parseJavaSwitchDefaultProofSource(t, genLang, src, "generated")
	refTree := parseJavaSwitchDefaultProofSource(t, refLang, src, "reference")
	genRoot := genTree.RootNode()
	refRoot := refTree.RootNode()

	if int(refRoot.EndByte()) != len(src) {
		t.Fatalf("reference root does not cover full source: %s", javaSwitchDefaultRootSummary(refRoot, refLang, src))
	}
	if refRoot.HasError() {
		t.Fatalf("reference root has error: %s\nREF: %s", javaSwitchDefaultRootSummary(refRoot, refLang, src), safeSExpr(refRoot, refLang, 256))
	}
	if int(genRoot.EndByte()) != len(src) {
		t.Fatalf("generated root does not cover full source: %s\nGEN: %s\nREF: %s",
			javaSwitchDefaultRootSummary(genRoot, genLang, src),
			safeSExpr(genRoot, genLang, 256),
			safeSExpr(refRoot, refLang, 256))
	}
	if genRoot.HasError() {
		t.Fatalf("generated root has error while reference is clean: %s\nGEN: %s\nREF: %s",
			javaSwitchDefaultRootSummary(genRoot, genLang, src),
			safeSExpr(genRoot, genLang, 256),
			safeSExpr(refRoot, refLang, 256))
	}
	divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 20)
	if len(divs) > 0 {
		t.Fatalf("generated/reference Java switch default mismatch: %s\n%s\nGEN: %s\nREF: %s",
			divs[0],
			javaSwitchDefaultRootSummary(genRoot, genLang, src),
			safeSExpr(genRoot, genLang, 256),
			safeSExpr(refRoot, refLang, 256))
	}
}

func parseJavaSwitchDefaultProofSource(t *testing.T, lang *gotreesitter.Language, src []byte, label string) *gotreesitter.Tree {
	t.Helper()

	tree, err := gotreesitter.NewParser(lang).Parse(src)
	if err != nil {
		t.Fatalf("%s parse failed: %v", label, err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatalf("%s parse missing root node", label)
	}
	return tree
}

func javaSwitchDefaultRootSummary(root *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	end := int(root.EndByte())
	if end < 0 {
		end = 0
	}
	if end > len(src) {
		end = len(src)
	}
	suffixEnd := end + 80
	if suffixEnd > len(src) {
		suffixEnd = len(src)
	}
	return fmt.Sprintf("type=%s range=%d..%d len=%d hasError=%v suffix=%q",
		root.Type(lang),
		root.StartByte(),
		root.EndByte(),
		len(src),
		root.HasError(),
		string(src[end:suffixEnd]))
}
