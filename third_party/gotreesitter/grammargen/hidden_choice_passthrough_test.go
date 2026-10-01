package grammargen

import "testing"

func TestBuildHiddenChoicePassthroughSymbolsMarksOnlyNeutralHiddenChoices(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "EOF", Kind: SymbolTerminal},
			{Name: "_choice", Kind: SymbolNonterminal, Named: true},
			{Name: "_sequence", Kind: SymbolNonterminal, Named: true},
			{Name: "leaf", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "_supertype", Kind: SymbolNonterminal, Named: true, Supertype: true},
			{Name: "_aliased", Kind: SymbolNonterminal, Named: true},
			{Name: "parent", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "_wrapper", Kind: SymbolNonterminal, Named: true},
		},
		Productions: []Production{
			{LHS: 1, RHS: []int{2}},
			{LHS: 1, RHS: []int{3}},
			{LHS: 2, RHS: []int{3, 0}},
			{LHS: 4, RHS: []int{3}},
			{LHS: 5, RHS: []int{3}},
			{LHS: 6, RHS: []int{5}, Aliases: []AliasInfo{{ChildIndex: 0, Name: "alias", Named: true}}},
			{LHS: 7, RHS: []int{3}},
		},
	}

	got := buildHiddenChoicePassthroughSymbols(ng, len(ng.Symbols))
	if len(got) != len(ng.Symbols) || !got[1] {
		t.Fatalf("_choice passthrough mark = %v, want true", got)
	}
	for _, sym := range []int{2, 4, 5, 7} {
		if got[sym] {
			t.Fatalf("symbol %s was marked passthrough; full table %v", ng.Symbols[sym].Name, got)
		}
	}
}

// TestBuildHiddenChoicePassthroughSymbolsWidenedConditionMarksSupertypeChoice
// exercises the (landed) widened condition needed for Go's `_statement` /
// `_simple_statement`: a hidden, named, multi-alternative choice symbol that
// is ALSO declared a Supertype, with every alternative a plain, unadorned
// Sym() reference (no Fields/Aliases anywhere). Supertype alone used to
// unconditionally disqualify the whole symbol; it no longer does, because
// Supertype is purely a node-types.json/query-predicate classification that
// doesn't require the wrapper node to physically exist (see
// query_predicates.go's supertypeContainsNodeSymbol, which matches against
// the surviving concrete descendant's own symbol).
func TestBuildHiddenChoicePassthroughSymbolsWidenedConditionMarksSupertypeChoice(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "EOF", Kind: SymbolTerminal},
			{Name: "_stmt", Kind: SymbolNonterminal, Named: true, Supertype: true},
			{Name: "return_stmt", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "if_stmt", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "_referenced_elsewhere", Kind: SymbolNonterminal, Named: true},
			{Name: "outer", Kind: SymbolNonterminal, Visible: true, Named: true},
		},
		Productions: []Production{
			{LHS: 1, RHS: []int{2}}, // _stmt -> return_stmt (plain)
			{LHS: 1, RHS: []int{3}}, // _stmt -> if_stmt (plain)
			{LHS: 4, RHS: []int{2}},
			{LHS: 4, RHS: []int{3}},
			{LHS: 5, RHS: []int{4}, Aliases: []AliasInfo{{ChildIndex: 0, Name: "alias", Named: true}}},
		},
	}

	got := buildHiddenChoicePassthroughSymbols(ng, len(ng.Symbols))
	if len(got) != len(ng.Symbols) || !got[1] {
		t.Fatalf("_stmt passthrough mark = %v, want true (widened condition)", got)
	}
	// _referenced_elsewhere has no fields/aliases of its own and is not a
	// supertype, but it IS aliased-into by `outer`, so it must still be
	// excluded regardless of the widening.
	if got[4] {
		t.Fatalf("_referenced_elsewhere was marked passthrough despite being alias-referenced; full table %v", got)
	}
}

// TestBuildHiddenChoicePassthroughSymbolsMixedAliasChoiceStaysExcluded pins the
// KILL-GATED half of the widening: a hidden, named, Supertype, multi-alternative
// choice symbol where ONE alternative carries a child alias (mirroring Go's
// `_expression -> Alias(choice("new", "make"), "identifier")` alongside twenty
// plain Sym() alternatives) must stay excluded. A prototype that also relaxed
// this (per-production, since the runtime already re-checks
// reduceProductionHasEffectiveFields/reduceAliasSequence(act.ProductionID) for
// the specific fired production) regressed Markdown's `_block_not_section`
// hidden choice on TestMarkdownGrammarMdppCorpusParity (emoji,
// superscript-subscript): `(document (section (paragraph (inline))))` lost its
// `paragraph` node. See buildHiddenChoicePassthroughSymbols's comment for the
// full account; this test just pins the safe (excluded) behavior.
func TestBuildHiddenChoicePassthroughSymbolsMixedAliasChoiceStaysExcluded(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "EOF", Kind: SymbolTerminal},
			{Name: "_expr", Kind: SymbolNonterminal, Named: true, Supertype: true},
			{Name: "binary_expr", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "identifier", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "new_tok", Kind: SymbolNonterminal, Named: false},
		},
		Productions: []Production{
			{LHS: 1, RHS: []int{2}},                                                                        // _expr -> binary_expr (plain)
			{LHS: 1, RHS: []int{3}},                                                                        // _expr -> identifier (plain)
			{LHS: 1, RHS: []int{4}, Aliases: []AliasInfo{{ChildIndex: 0, Name: "identifier", Named: true}}}, // _expr -> new_tok, aliased to "identifier"
		},
	}

	got := buildHiddenChoicePassthroughSymbols(ng, len(ng.Symbols))
	if got != nil && got[1] {
		t.Fatalf("_expr (mixed alias choice) was marked passthrough; want excluded. full table %v", got)
	}
}
