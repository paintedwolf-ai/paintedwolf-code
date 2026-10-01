package grammargen

import (
	"fmt"
	"os"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestTypeScriptTypeAssertionOverTernaryParity(t *testing.T) {
	if raceEnabled {
		t.Skip("skip heavyweight TypeScript parity generation under -race; non-race coverage keeps the generated-vs-reference check")
	}
	genLang, refLang := loadImportedParityLanguages(t, "typescript")

	tests := []struct {
		name string
		src  string
	}{
		{
			name: "compact_close_angles",
			src:  "namespace ts {\n    function createNodeArray<T extends Node>(elements: T[], pos: number, end?: number): NodeArray<T> {\n        const length = elements.length;\n        const array = <MutableNodeArray<T>>(length >= 1 && length <= 4 ? elements.slice() : elements);\n        return array;\n    }\n}\n",
		},
		{
			name: "spaced_close_angles",
			src:  "namespace ts {\n    function createNodeArray<T extends Node>(elements: T[], pos: number, end?: number): NodeArray<T> {\n        const length = elements.length;\n        const array = <MutableNodeArray<T> >(length >= 1 && length <= 4 ? elements.slice() : elements);\n        return array;\n    }\n}\n",
		},
		{
			name: "compact_close_angles_before_identifier_call",
			src:  "namespace ts {\n    function parseNamedImportsOrExports(kind: SyntaxKind) {\n        const node = createNode(kind);\n        node.elements = <NodeArray<ImportSpecifier> | NodeArray<ExportSpecifier>>parseBracketedList(ParsingContext.ImportOrExportSpecifiers,\n            kind === SyntaxKind.NamedImports ? parseImportSpecifier : parseExportSpecifier,\n            SyntaxKind.OpenBraceToken, SyntaxKind.CloseBraceToken);\n        return finishNode(node);\n    }\n}\n",
		},
		{
			name: "spaced_close_angles_before_identifier_call",
			src:  "namespace ts {\n    function parseNamedImportsOrExports(kind: SyntaxKind) {\n        const node = createNode(kind);\n        node.elements = <NodeArray<ImportSpecifier> | NodeArray<ExportSpecifier> >parseBracketedList(ParsingContext.ImportOrExportSpecifiers,\n            kind === SyntaxKind.NamedImports ? parseImportSpecifier : parseExportSpecifier,\n            SyntaxKind.OpenBraceToken, SyntaxKind.CloseBraceToken);\n        return finishNode(node);\n    }\n}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := []byte(tt.src)

			genTree, err := gotreesitter.NewParser(genLang).Parse(src)
			if err != nil {
				t.Fatalf("generated parse: %v", err)
			}
			refTree, err := gotreesitter.NewParser(refLang).Parse(src)
			if err != nil {
				t.Fatalf("reference parse: %v", err)
			}

			genRoot := genTree.RootNode()
			refRoot := refTree.RootNode()
			genSExpr := safeSExpr(genRoot, genLang, 192)
			refSExpr := safeSExpr(refRoot, refLang, 192)

			if genRoot.HasError() != refRoot.HasError() {
				if os.Getenv("DIAG_TS_TYPE_ASSERTION") == "1" {
					logTypeAssertionDiag(t, genLang, src)
				}
				t.Fatalf("error mismatch: gen=%v ref=%v\nGEN: %s\nREF: %s", genRoot.HasError(), refRoot.HasError(), genSExpr, refSExpr)
			}
			if genSExpr != refSExpr {
				if os.Getenv("DIAG_TS_TYPE_ASSERTION") == "1" {
					logTypeAssertionDiag(t, genLang, src)
				}
				divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 8)
				t.Fatalf("sexpr mismatch\nGEN: %s\nREF: %s\nDIVS: %v", genSExpr, refSExpr, divs)
			}
		})
	}
}

func logTypeAssertionDiag(t *testing.T, lang *gotreesitter.Language, src []byte) {
	t.Helper()
	parser := gotreesitter.NewParser(lang)
	parser.SetLogger(func(kind gotreesitter.ParserLogType, msg string) {
		if kind != gotreesitter.ParserLogLex {
			return
		}
		var sym, start, end int
		if _, err := fmt.Sscanf(msg, "token sym=%d start=%d end=%d", &sym, &start, &end); err != nil {
			t.Logf("lex: %s", msg)
			return
		}
		if sym < 0 || sym >= len(lang.SymbolNames) || start < 0 || end < start || end > len(src) {
			t.Logf("lex: %s", msg)
			return
		}
		t.Logf("lex sym=%d raw=%q text=%q start=%d end=%d", sym, lang.SymbolNames[sym], string(src[start:end]), start, end)
	})
	tree, err := parser.Parse(src)
	if err != nil {
		t.Logf("diag parse error: %v", err)
		return
	}
	t.Logf("diag parse hasError=%v sexpr=%s", tree.RootNode().HasError(), safeSExpr(tree.RootNode(), lang, 192))
}
