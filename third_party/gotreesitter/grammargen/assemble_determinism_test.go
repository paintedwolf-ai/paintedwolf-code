package grammargen

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestBuildParseTablesOrdersMapBackedActionsDeterministically(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "end", Kind: SymbolTerminal},
			{Name: "a", Kind: SymbolTerminal},
			{Name: "b", Kind: SymbolTerminal},
			{Name: "S", Kind: SymbolNonterminal},
		},
		Productions: []Production{
			{LHS: 3, RHS: []int{1}, ProductionID: 0},
		},
	}
	tables := &LRTables{
		StateCount: 2,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				2: {{kind: lrShift, state: 1}},
				1: {{kind: lrReduce, prodIdx: 0}},
			},
			1: {},
		},
		GotoTable: map[int]map[int]int{
			0: {3: 1},
			1: {},
		},
	}
	lang := &gotreesitter.Language{
		LexModes: make([]gotreesitter.LexMode, tables.StateCount),
	}

	if err := buildParseTables(lang, tables, ng, ng.TokenCount()); err != nil {
		t.Fatalf("buildParseTables: %v", err)
	}

	if got, want := len(lang.ParseActions), 3; got != want {
		t.Fatalf("len(ParseActions) = %d, want %d", got, want)
	}
	if got, want := lang.ParseTable[1][1], uint16(1); got != want {
		t.Fatalf("action index for symbol a = %d, want %d", got, want)
	}
	if got, want := lang.ParseTable[1][2], uint16(2); got != want {
		t.Fatalf("action index for symbol b = %d, want %d", got, want)
	}
	if got, want := lang.ParseTable[1][3], uint16(2); got != want {
		t.Fatalf("goto state for symbol S = %d, want %d", got, want)
	}
	if got, want := lang.ParseActions[1].Actions[0].Type, gotreesitter.ParseActionReduce; got != want {
		t.Fatalf("ParseActions[1] type = %v, want %v", got, want)
	}
	if got, want := lang.ParseActions[2].Actions[0].Type, gotreesitter.ParseActionShift; got != want {
		t.Fatalf("ParseActions[2] type = %v, want %v", got, want)
	}
}

func TestBuildProductionSignaturesKeepsRHSShapeSeparateFromProductionID(t *testing.T) {
	ng := &NormalizedGrammar{
		Productions: []Production{
			{LHS: 3, RHS: []int{1, 2}, ProductionID: 0},
			{LHS: 3, RHS: []int{1, 2}, ProductionID: 0},
			{LHS: 4, RHS: []int{1, 2}, ProductionID: 0},
			{LHS: 3, RHS: []int{2, 1}, ProductionID: 0},
			{LHS: 3, RHS: []int{1, 2}, ProductionID: 7},
		},
	}
	lang := &gotreesitter.Language{}

	buildProductionSignatures(lang, ng)

	if got, want := len(lang.ProductionSignatures), 4; got != want {
		t.Fatalf("len(ProductionSignatures) = %d, want %d", got, want)
	}
	assertSignature := func(i int, lhs gotreesitter.Symbol, prodID uint16, rhs ...gotreesitter.Symbol) {
		t.Helper()
		sig := lang.ProductionSignatures[i]
		if sig.LHS != lhs || sig.ProductionID != prodID || len(sig.RHS) != len(rhs) {
			t.Fatalf("signature[%d] = {lhs:%d prod:%d rhs:%v}, want lhs:%d prod:%d rhs:%v", i, sig.LHS, sig.ProductionID, sig.RHS, lhs, prodID, rhs)
		}
		for j := range rhs {
			if sig.RHS[j] != rhs[j] {
				t.Fatalf("signature[%d].RHS[%d] = %d, want %d", i, j, sig.RHS[j], rhs[j])
			}
		}
	}
	assertSignature(0, 3, 0, 1, 2)
	assertSignature(1, 4, 0, 1, 2)
	assertSignature(2, 3, 0, 2, 1)
	assertSignature(3, 3, 7, 1, 2)
}
