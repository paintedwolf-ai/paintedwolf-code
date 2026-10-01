package grammargen

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestCPrimitiveTypeParameterParity(t *testing.T) {
	genLang, refLang := loadGeneratedCLanguage(t)
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "prototype",
			src:  "clusterNode *createClusterNode(char *nodename, int flags);\n",
		},
		{
			name: "definition",
			src:  "clusterNode *createClusterNode(char *nodename, int flags) { return 0; }\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			genTree, err := gotreesitter.NewParser(genLang).Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("gen parse: %v", err)
			}
			defer genTree.Release()

			refTree, err := gotreesitter.NewParser(refLang).Parse([]byte(tc.src))
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

			genSexp := normalizeSexp(genRoot.SExpr(genLang))
			refSexp := normalizeSexp(refRoot.SExpr(refLang))
			if genSexp != refSexp {
				t.Fatalf("parity mismatch:\n  gen: %s\n  ref: %s", genRoot.SExpr(genLang), refRoot.SExpr(refLang))
			}
		})
	}
}
