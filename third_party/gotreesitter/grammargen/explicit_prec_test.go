package grammargen

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestFlattenRulePreservesExplicitZeroPrecedence(t *testing.T) {
	st := newSymbolTable()
	lhs := st.addSymbol("start", SymbolInfo{Name: "start", Kind: SymbolNonterminal})

	prodID := 0
	prods := flattenRule2(Prec(0, Str("x")), lhs, st, &prodID)
	if len(prods) != 1 {
		t.Fatalf("flattenRule2 produced %d productions, want 1", len(prods))
	}
	if prods[0].Prec != 0 {
		t.Fatalf("production precedence = %d, want 0", prods[0].Prec)
	}
	if !prods[0].HasExplicitPrec {
		t.Fatal("production lost explicit prec(0, ...) metadata")
	}
}

func TestFlattenRulePreservesExplicitZeroPrecedenceInsideSeq(t *testing.T) {
	st := newSymbolTable()
	lhs := st.addSymbol("start", SymbolInfo{Name: "start", Kind: SymbolNonterminal})

	prodID := 0
	prods := flattenRule2(Seq(Str("a"), Prec(0, Str("b"))), lhs, st, &prodID)
	if len(prods) != 1 {
		t.Fatalf("flattenRule2 produced %d productions, want 1", len(prods))
	}
	if prods[0].Prec != 0 {
		t.Fatalf("production precedence = %d, want 0", prods[0].Prec)
	}
	if !prods[0].HasExplicitPrec {
		t.Fatal("seq production lost explicit inner prec(0, ...) metadata")
	}
}

func TestFlattenRuleOuterSequencePrecedenceBeatsInlinedChildPrecedence(t *testing.T) {
	st := newSymbolTable()
	lhs := st.addSymbol("binary_expression", SymbolInfo{Name: "binary_expression", Kind: SymbolNonterminal})
	st.addSymbol("left", SymbolInfo{Name: "left", Kind: SymbolNonterminal})
	st.addSymbol("+", SymbolInfo{Name: "+", Kind: SymbolTerminal})
	st.addSymbol("right", SymbolInfo{Name: "right", Kind: SymbolNonterminal})

	prodID := 0
	prods := flattenRule2(PrecLeft(13, Seq(
		Prec(1, Sym("left")),
		Str("+"),
		Prec(1, Sym("right")),
	)), lhs, st, &prodID)
	if len(prods) != 1 {
		t.Fatalf("flattenRule2 produced %d productions, want 1", len(prods))
	}
	if prods[0].Prec != 13 || prods[0].Assoc != AssocLeft || !prods[0].HasExplicitPrec {
		t.Fatalf("production precedence = %+v, want outer prec.left(13)", prods[0])
	}
}

func TestFlattenRuleChoiceAltPrecedenceBeatsInlinedChildPrecedence(t *testing.T) {
	st := newSymbolTable()
	lhs := st.addSymbol("expression", SymbolInfo{Name: "expression", Kind: SymbolNonterminal})
	st.addSymbol("left", SymbolInfo{Name: "left", Kind: SymbolNonterminal})
	st.addSymbol("+", SymbolInfo{Name: "+", Kind: SymbolTerminal})
	st.addSymbol("right", SymbolInfo{Name: "right", Kind: SymbolNonterminal})
	st.addSymbol("number", SymbolInfo{Name: "number", Kind: SymbolTerminal})

	prodID := 0
	prods := flattenRule2(Choice(
		PrecLeft(13, Seq(
			Prec(1, Sym("left")),
			Str("+"),
			Prec(1, Sym("right")),
		)),
		Str("number"),
	), lhs, st, &prodID)
	if len(prods) != 2 {
		t.Fatalf("flattenRule2 produced %d productions, want 2", len(prods))
	}
	for _, prod := range prods {
		if len(prod.RHS) == 3 {
			if prod.Prec != 13 || prod.Assoc != AssocLeft || !prod.HasExplicitPrec {
				t.Fatalf("choice-alt production precedence = %+v, want outer prec.left(13)", prod)
			}
			return
		}
	}
	t.Fatalf("missing binary production: %+v", prods)
}

func TestResolveActionConflictExplicitNegativeShiftBeatsImplicitZeroReduce(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "=", Kind: SymbolTerminal},
			{Name: "primary_expression", Kind: SymbolNonterminal},
			{Name: "assignment_expression", Kind: SymbolNonterminal},
		},
		Productions: []Production{
			{LHS: 1},
		},
	}

	actions := []lrAction{
		{kind: lrReduce, prodIdx: 0, lhsSym: 1},
		{kind: lrShift, state: 7, prec: -2, hasPrec: true, assoc: AssocRight, lhsSym: 2},
	}

	got, err := resolveActionConflict(0, actions, ng)
	if err != nil {
		t.Fatalf("resolveActionConflict: %v", err)
	}
	if len(got) != 1 || got[0].kind != lrShift {
		t.Fatalf("resolved actions = %+v, want single shift", got)
	}
}

func TestResolveActionConflictJuliaAssignmentOperatorAliasShift(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "operator", Kind: SymbolTerminal},
			{Name: "_expression", Kind: SymbolNonterminal},
			{Name: "assignment", Kind: SymbolNonterminal},
		},
		Productions: []Production{
			{LHS: 1},
		},
	}

	actions := []lrAction{
		{kind: lrReduce, prodIdx: 0, lhsSym: 1},
		{kind: lrShift, state: 7, prec: -2, hasPrec: true, assoc: AssocRight, lhsSym: 2},
	}

	got, err := resolveActionConflict(0, actions, ng)
	if err != nil {
		t.Fatalf("resolveActionConflict: %v", err)
	}
	if len(got) != 1 || got[0].kind != lrShift {
		t.Fatalf("resolved actions = %+v, want single shift", got)
	}
}

func TestResolveActionConflictExplicitZeroReduceStillBeatsNegativeShift(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "=", Kind: SymbolTerminal},
			{Name: "primary_expression", Kind: SymbolNonterminal},
			{Name: "expression_list", Kind: SymbolNonterminal},
		},
		Productions: []Production{
			{LHS: 1, HasExplicitPrec: true},
		},
	}

	actions := []lrAction{
		{kind: lrReduce, prodIdx: 0, lhsSym: 1},
		{kind: lrShift, state: 9, prec: -1, hasPrec: true, assoc: AssocRight, lhsSym: 2},
	}

	got, err := resolveActionConflict(0, actions, ng)
	if err != nil {
		t.Fatalf("resolveActionConflict: %v", err)
	}
	if len(got) != 1 || got[0].kind != lrReduce {
		t.Fatalf("resolved actions = %+v, want single reduce", got)
	}
}

func TestResolveActionConflictNegativeShiftGuardIsAssignmentOnly(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: ":", Kind: SymbolTerminal},
			{Name: "primary_expression", Kind: SymbolNonterminal},
			{Name: "conditional_expression", Kind: SymbolNonterminal},
		},
		Productions: []Production{
			{LHS: 1},
		},
	}

	actions := []lrAction{
		{kind: lrReduce, prodIdx: 0, lhsSym: 1},
		{kind: lrShift, state: 11, prec: -1, hasPrec: true, assoc: AssocRight, lhsSym: 2},
	}

	got, err := resolveActionConflict(0, actions, ng)
	if err != nil {
		t.Fatalf("resolveActionConflict: %v", err)
	}
	if len(got) != 1 || got[0].kind != lrReduce {
		t.Fatalf("resolved actions = %+v, want single reduce", got)
	}
}

func TestResolveReduceReduceExplicitZeroBeatsImplicitZero(t *testing.T) {
	ng := &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: ")", Kind: SymbolTerminal},
			{Name: "primary_expression", Kind: SymbolNonterminal},
			{Name: "type_query", Kind: SymbolNonterminal},
		},
		Productions: []Production{
			{LHS: 1},
			{LHS: 2, HasExplicitPrec: true, Assoc: AssocRight},
		},
	}

	actions := []lrAction{
		{kind: lrReduce, prodIdx: 0, lhsSym: 1},
		{kind: lrReduce, prodIdx: 1, lhsSym: 2},
	}

	got, err := resolveActionConflict(0, actions, ng)
	if err != nil {
		t.Fatalf("resolveActionConflict: %v", err)
	}
	if len(got) != 1 || got[0].kind != lrReduce || got[0].prodIdx != 1 {
		t.Fatalf("resolved actions = %+v, want explicit reduce prod 1", got)
	}
}

func TestFlattenRuleSeqBlankChoiceDoesNotInheritExplicitZeroPrecedence(t *testing.T) {
	st := newSymbolTable()
	lhs := st.addSymbol("start", SymbolInfo{Name: "start", Kind: SymbolNonterminal})
	st.addSymbol("a", SymbolInfo{Name: "a", Kind: SymbolTerminal})
	st.addSymbol("x", SymbolInfo{Name: "x", Kind: SymbolTerminal})

	prodID := 0
	prods := flattenRule2(PrecRight(0, Seq(Str("a"), Choice(Str("x"), Blank()))), lhs, st, &prodID)
	if len(prods) != 2 {
		t.Fatalf("flattenRule2 produced %d productions, want 2", len(prods))
	}

	var sawConcrete, sawBlankArm bool
	for _, p := range prods {
		switch len(p.RHS) {
		case 2:
			sawConcrete = true
			if !p.HasExplicitPrec || p.Assoc != AssocRight {
				t.Fatalf("concrete alternative lost explicit precedence metadata: %+v", p)
			}
		case 1:
			sawBlankArm = true
			if p.HasExplicitPrec || p.Assoc != AssocNone || p.Prec != 0 {
				t.Fatalf("blank-arm alternative inherited explicit zero precedence unexpectedly: %+v", p)
			}
		default:
			t.Fatalf("unexpected production shape: %+v", p)
		}
	}
	if !sawConcrete || !sawBlankArm {
		t.Fatalf("missing expected concrete/blank-arm productions: %+v", prods)
	}
}

func TestFlattenRuleSeqTrailingAutomaticSemicolonKeepsExplicitZeroPrecedence(t *testing.T) {
	st := newSymbolTable()
	lhs := st.addSymbol("statement_block", SymbolInfo{Name: "statement_block", Kind: SymbolNonterminal})
	st.addSymbol("{", SymbolInfo{Name: "{", Kind: SymbolTerminal})
	st.addSymbol("}", SymbolInfo{Name: "}", Kind: SymbolTerminal})
	st.addSymbol("statement", SymbolInfo{Name: "statement", Kind: SymbolNonterminal})
	st.addSymbol("_automatic_semicolon", SymbolInfo{Name: "_automatic_semicolon", Kind: SymbolTerminal})

	prodID := 0
	prods := flattenRule2(PrecRight(0, Seq(
		Str("{"),
		Sym("statement"),
		Str("}"),
		Choice(Sym("_automatic_semicolon"), Blank()),
	)), lhs, st, &prodID)
	if len(prods) != 2 {
		t.Fatalf("flattenRule2 produced %d productions, want 2", len(prods))
	}

	var sawWithSemi, sawWithoutSemi bool
	for _, p := range prods {
		switch len(p.RHS) {
		case 4:
			sawWithSemi = true
			if !p.HasExplicitPrec || p.Assoc != AssocRight {
				t.Fatalf("semicolon alternative lost explicit zero precedence metadata: %+v", p)
			}
		case 3:
			sawWithoutSemi = true
			if !p.HasExplicitPrec || p.Assoc != AssocRight {
				t.Fatalf("blank-suffix alternative lost explicit zero precedence metadata: %+v", p)
			}
		default:
			t.Fatalf("unexpected production shape: %+v", p)
		}
	}
	if !sawWithSemi || !sawWithoutSemi {
		t.Fatalf("missing expected trailing-semicolon alternatives: %+v", prods)
	}
}

func TestFlattenRuleChoiceNonterminalWrapperDoesNotInheritExplicitZeroAssoc(t *testing.T) {
	st := newSymbolTable()
	lhs := st.addSymbol("_string", SymbolInfo{Name: "_string", Kind: SymbolNonterminal})
	st.addSymbol("string_literal", SymbolInfo{Name: "string_literal", Kind: SymbolTerminal})
	st.addSymbol("concatenated_string", SymbolInfo{Name: "concatenated_string", Kind: SymbolNonterminal})

	prodID := 0
	prods := flattenRule2(PrecLeft(0, Choice(Sym("string_literal"), Sym("concatenated_string"))), lhs, st, &prodID)
	if len(prods) != 2 {
		t.Fatalf("flattenRule2 produced %d productions, want 2", len(prods))
	}

	var sawTerminal, sawNonterminal bool
	for _, p := range prods {
		if len(p.RHS) != 1 {
			t.Fatalf("unexpected production shape: %+v", p)
		}
		rhsName := st.symbols[p.RHS[0]].Name
		switch rhsName {
		case "string_literal":
			sawTerminal = true
			if !p.HasExplicitPrec || p.Assoc != AssocLeft {
				t.Fatalf("terminal wrapper lost explicit zero precedence metadata: %+v", p)
			}
		case "concatenated_string":
			sawNonterminal = true
			if p.HasExplicitPrec || p.Assoc != AssocNone {
				t.Fatalf("nonterminal wrapper inherited explicit zero precedence unexpectedly: %+v", p)
			}
		default:
			t.Fatalf("unexpected rhs %q in production %+v", rhsName, p)
		}
	}
	if !sawTerminal || !sawNonterminal {
		t.Fatalf("missing expected wrapper productions: %+v", prods)
	}
}

func TestParseExplicitZeroOptionalBlankStillShiftsConcreteBranch(t *testing.T) {
	g := NewGrammar("explicit_zero_optional_blank")
	g.Rules["program"] = Seq(Sym("wrapped"), Str("!"))
	g.Rules["wrapped"] = PrecRight(0, Seq(Str("a"), Choice(Sym("block"), Blank())))
	g.Rules["block"] = Seq(Str("{"), Str("}"))
	g.RuleOrder = []string{"program", "wrapped", "block"}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte("a{}!"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tree.RootNode().HasError() {
		t.Fatalf("parse has error: %s", tree.ParseRuntime().Summary())
	}
}
