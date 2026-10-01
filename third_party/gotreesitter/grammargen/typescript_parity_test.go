package grammargen

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestTypeScriptGenericCallParity(t *testing.T) {
	assertTypeScriptGeneratedMatchesReference(t,
		"namespace ts {\n    namespace Parser {\n        function initializeState() {\n            identifiers = createMap<string>();\n        }\n    }\n}\n",
		128,
		8,
		true,
	)
}

func TestTypeScriptConditionalTypeParity(t *testing.T) {
	assertTypeScriptGeneratedMatchesReference(t, "type T = X extends Y ? Z : Y\n", 128, 8, true)
}

func TestTypeScriptTernaryAfterLogicalOrParity(t *testing.T) {
	assertTypeScriptGeneratedMatchesReference(t,
		"namespace ts {\n    namespace Parser {\n        function getLanguageVariant(scriptKind: ScriptKind) {\n            return scriptKind === ScriptKind.TSX || scriptKind === ScriptKind.JSX || scriptKind === ScriptKind.JS || scriptKind === ScriptKind.JSON ? LanguageVariant.JSX : LanguageVariant.Standard;\n        }\n    }\n}\n",
		128,
		8,
		false,
	)
}

func TestTypeScriptNamespaceConstEnumParity(t *testing.T) {
	assertTypeScriptGeneratedMatchesReference(t,
		"/// <reference path=\"utilities.ts\"/>\n/// <reference path=\"scanner.ts\"/>\n\nnamespace ts {\n    const enum SignatureFlags {\n        None = 0,\n    }\n}\n",
		64,
		8,
		false,
	)
}

func assertTypeScriptGeneratedMatchesReference(t *testing.T, src string, sexprDepth, compareDepth int, allowEmptyDivergence bool) {
	t.Helper()
	if raceEnabled {
		t.Skip("skip heavyweight TypeScript parity generation under -race; non-race coverage keeps the generated-vs-reference check")
	}

	genLang, refLang := loadImportedParityLanguages(t, "typescript")
	genTree, err := gotreesitter.NewParser(genLang).Parse([]byte(src))
	if err != nil {
		t.Fatalf("generated parse: %v", err)
	}
	refTree, err := gotreesitter.NewParser(refLang).Parse([]byte(src))
	if err != nil {
		t.Fatalf("reference parse: %v", err)
	}

	genRoot := genTree.RootNode()
	refRoot := refTree.RootNode()
	genSExpr := safeSExpr(genRoot, genLang, sexprDepth)
	refSExpr := safeSExpr(refRoot, refLang, sexprDepth)

	if genRoot.HasError() != refRoot.HasError() {
		t.Fatalf("error mismatch: gen=%v ref=%v\nGEN: %s\nREF: %s", genRoot.HasError(), refRoot.HasError(), genSExpr, refSExpr)
	}
	if genSExpr != refSExpr {
		divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", compareDepth)
		if allowEmptyDivergence && len(divs) == 0 {
			return
		}
		t.Fatalf("sexpr mismatch\nGEN: %s\nREF: %s\nDIVS: %v", genSExpr, refSExpr, divs)
	}
}
