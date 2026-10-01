package grammargen

import (
	"errors"
	"strings"
	"testing"
)

// TestExtraChainSyntheticStateBudgetFormula unit-tests the budget math in
// isolation: max(multiplier * mainStateCount, floor), with an env override
// for testing/diagnosis. See GEN_COST_RCA (wave7) and the doc comment on
// extraChainSyntheticStateBudget for how the default multiplier/floor were
// chosen from measured fleet data (cobol's copy_statement extra is the
// largest observed legitimate nonterminal-extra chain: 22027 main states ->
// 171240 synthetic states, ~7.8x).
func TestExtraChainSyntheticStateBudgetFormula(t *testing.T) {
	t.Run("floor dominates for small grammars", func(t *testing.T) {
		got := extraChainSyntheticStateBudget(1000)
		want := extraChainSyntheticStateFloor
		if got != want {
			t.Errorf("extraChainSyntheticStateBudget(1000) = %d, want floor %d", got, want)
		}
	})

	t.Run("multiplier dominates for large grammars", func(t *testing.T) {
		// 22027 main states mirrors cobol's measured mainStateCount.
		got := extraChainSyntheticStateBudget(22027)
		want := 22027 * extraChainSyntheticStateMultiplier
		if got != want {
			t.Errorf("extraChainSyntheticStateBudget(22027) = %d, want %d", got, want)
		}
		// cobol legitimately needs 171240 synthetic states; the computed cap
		// must clear that with real headroom or the guard would break cobol.
		const cobolObservedSynthetic = 171240
		if got <= cobolObservedSynthetic {
			t.Fatalf("computed budget %d does not clear cobol's observed synthetic-state count %d", got, cobolObservedSynthetic)
		}
		// Headroom regression: an adversarial review of PR #212 asked the
		// multiplier to be raised from 12x to 24x for more margin over cobol's
		// legitimate 7.8x ratio. Guard against silently drifting back down.
		const wantHeadroomMultiple = 3
		if got < cobolObservedSynthetic*wantHeadroomMultiple {
			t.Errorf("computed budget %d clears cobol's observed count %d by less than %dx headroom (got %.2fx)",
				got, cobolObservedSynthetic, wantHeadroomMultiple, float64(got)/float64(cobolObservedSynthetic))
		}
	})

	t.Run("env override wins regardless of mainStateCount", func(t *testing.T) {
		t.Setenv(extraChainStateBudgetEnv, "7")
		if got := extraChainSyntheticStateBudget(1_000_000); got != 7 {
			t.Errorf("extraChainSyntheticStateBudget(1_000_000) with override = %d, want 7", got)
		}
	})

	t.Run("invalid env override falls back to the computed default", func(t *testing.T) {
		for _, raw := range []string{"0", "-5", "not-a-number", ""} {
			t.Setenv(extraChainStateBudgetEnv, raw)
			got := extraChainSyntheticStateBudget(1000)
			want := extraChainSyntheticStateFloor
			if got != want {
				t.Errorf("extraChainSyntheticStateBudget(1000) with override %q = %d, want fallback %d", raw, got, want)
			}
		}
	})
}

func TestUseLALRNonterminalExtraStatesDispatch(t *testing.T) {
	t.Run("recursive heredoc interpolation uses bounded item sets", func(t *testing.T) {
		ng := &NormalizedGrammar{
			Symbols: []SymbolInfo{
				{Name: "end", Kind: SymbolTerminal},
				{Name: "heredoc_start", Kind: SymbolTerminal},
				{Name: "heredoc_body", Kind: SymbolNonterminal, IsExtra: true},
				{Name: "heredoc_parts", Kind: SymbolNonterminal},
				{Name: "interpolation", Kind: SymbolNonterminal},
			},
			ExtraSymbols: []int{2},
			Productions: []Production{
				{LHS: 2, RHS: []int{1, 3}, IsExtra: true},
				{LHS: 3, RHS: []int{4}},
			},
		}
		if !useLALRNonterminalExtraStates(ng) {
			t.Fatal("recursive heredoc_body -> interpolation shape must use bounded LALR item sets")
		}
	})

	t.Run("crystal heredoc interpolation uses bounded item sets", func(t *testing.T) {
		ng := &NormalizedGrammar{
			GrammarName: "crystal",
			Symbols: []SymbolInfo{
				{Name: "end", Kind: SymbolTerminal},
				{Name: "heredoc_start", Kind: SymbolTerminal},
				{Name: "heredoc_body", Kind: SymbolNonterminal, IsExtra: true},
				{Name: "heredoc_parts", Kind: SymbolNonterminal},
				{Name: "interpolation", Kind: SymbolNonterminal},
				{Name: "_expression", Kind: SymbolNonterminal},
			},
			ExtraSymbols: []int{2},
			Productions: []Production{
				{LHS: 2, RHS: []int{1, 3}, IsExtra: true},
				{LHS: 3, RHS: []int{4}},
				{LHS: 4, RHS: []int{5}},
			},
		}
		if !useLALRNonterminalExtraStates(ng) {
			t.Fatal("crystal heredoc_body -> interpolation -> _expression must use bounded LALR item sets")
		}
	})

	t.Run("unrelated directive extra keeps legacy builder", func(t *testing.T) {
		ng := &NormalizedGrammar{
			Symbols: []SymbolInfo{
				{Name: "end", Kind: SymbolTerminal},
				{Name: "hash", Kind: SymbolTerminal},
				{Name: "directive", Kind: SymbolNonterminal, IsExtra: true},
				{Name: "heredoc_body", Kind: SymbolNonterminal},
				{Name: "interpolation", Kind: SymbolNonterminal},
			},
			ExtraSymbols: []int{2},
			Productions: []Production{
				{LHS: 2, RHS: []int{1}, IsExtra: true},
				{LHS: 3, RHS: []int{4}},
			},
		}
		if useLALRNonterminalExtraStates(ng) {
			t.Fatal("a non-extra heredoc_body must not move unrelated directive grammars off the legacy path")
		}
	})
}

// TestNonterminalExtraChainSyntheticStateBudgetTripsFast is the regression
// test requested alongside the ruby heredoc_body fix: a synthetic grammar
// with the same pathological shape as ruby's preserved heredoc_body (a
// nonterminal grammar extra whose body embeds a statement-embedding
// alternative - "interpolation" re-entering the full statement grammar, the
// same relationship as ruby's interpolation -> _statements) must hard-fail
// fast and clearly once an artificially tiny synthetic-state budget is
// exceeded, instead of growing unboundedly.
//
// This grammar is small enough that it fully converges on its own (32
// synthetic states after core merging - see the "natural" subtest), so this
// test stays fast even if the budget guard were ever disabled; the budget
// override still proves the guard trips before reaching that natural size.
//
// The guard still panics at the exact state-allocation boundary (see
// ExtraChainSyntheticStateBudgetError), and the raw-construction subtest below
// exercises it directly. But an adversarial review of PR #212 found that panic escaped
// every public entry point (Generate/GenerateLanguage/GenerateLanguageAndBlob)
// uncaught, crashing real consumers (taproot's runtime fallback, the wasm and
// CLI generation commands, the grammar registry) instead of returning an
// error like every sibling budget guard in this package does. The
// "guarded wrapper" and "public API" subtests below are the regression
// coverage for that fix: addNonterminalExtraChainsGuarded is now the sole
// recover point, called from generateWithReportCtx, and it must convert only
// *ExtraChainSyntheticStateBudgetError to a normal returned error while
// re-panicking anything else unchanged.
func TestNonterminalExtraChainSyntheticStateBudgetTripsFast(t *testing.T) {
	const tinyBudget = 8

	newBudgetGrammar := func() *Grammar {
		g := NewGrammar("ruby")
		g.Define("source_file", Repeat(Sym("statement")))
		g.Define("statement", Choice(
			Sym("if_statement"),
			Sym("while_statement"),
			Sym("assign_statement"),
			Sym("word"),
		))
		g.Define("word", Pat(`[a-z]+`))
		g.Define("if_statement", Seq(Str("if"), Sym("word"), Str("then"), Sym("statement")))
		g.Define("while_statement", Seq(Str("while"), Sym("word"), Str("do"), Sym("statement")))
		g.Define("assign_statement", Seq(Sym("word"), Str("="), Sym("statement")))
		// interpolation mirrors ruby's interpolation -> _statements:
		// a nonterminal-extra alternative that re-enters the entire statement
		// grammar rather than staying scanner-delimited.
		g.Define("interpolation", Seq(Str("#{"), Optional(Sym("statement")), Str("}")))
		g.Define("heredoc_content", Pat(`[^#}]+`))
		// heredoc_body mirrors ruby's exact shape:
		// SEQ(start, REPEAT(CHOICE(content, interpolation)), end), declared as a
		// nonterminal grammar extra (see GEN_COST_RCA).
		g.Define("heredoc_body", Seq(
			Str("<<~X"),
			Repeat(Choice(Sym("heredoc_content"), Sym("interpolation"))),
			Str("X_END"),
		))
		g.SetExtras(Pat(`\s`), Sym("heredoc_body"))
		// Exercise the same post-import hook as ImportGrammarJSON. A Ruby shape
		// hint must never make generation fit by deleting interpolation.
		applyImportGrammarPostShapeHints(g)
		return g
	}

	buildBudgetGrammar := func() (*NormalizedGrammar, *LRTables, *lrContext) {
		ng, err := Normalize(newBudgetGrammar())
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		tables, ctx, err := buildLRTablesWithProvenance(ng)
		if err != nil {
			t.Fatalf("build LR tables: %v", err)
		}
		return ng, tables, ctx
	}

	// checkBudgetErr asserts the common shape every path below should
	// eventually surface, whether recovered from a panic or returned as an
	// ordinary error: the named type, unwrappable via errors.As, naming the
	// offending symbol/grammar/counts.
	checkBudgetErr := func(t *testing.T, err error, tinyBudget int) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error once the synthetic-state budget was exceeded, got nil")
		}
		var budgetErr *ExtraChainSyntheticStateBudgetError
		if !errors.As(err, &budgetErr) {
			t.Fatalf("expected err to wrap *ExtraChainSyntheticStateBudgetError (via errors.As), got %v (%T)", err, err)
		}
		if budgetErr.Budget != tinyBudget {
			t.Errorf("Budget = %d, want %d", budgetErr.Budget, tinyBudget)
		}
		if budgetErr.SyntheticCount < budgetErr.Budget {
			t.Errorf("SyntheticCount (%d) should be >= Budget (%d) at the point of failure", budgetErr.SyntheticCount, budgetErr.Budget)
		}
		if budgetErr.Grammar != "ruby" {
			t.Errorf("Grammar = %q, want %q", budgetErr.Grammar, "ruby")
		}
		if budgetErr.Symbol == "" || budgetErr.Symbol == "<unknown>" {
			t.Errorf("expected the error to name the offending nonterminal-extra symbol, got %q", budgetErr.Symbol)
		}
		if !strings.Contains(budgetErr.Symbol, "heredoc_body") {
			t.Errorf("expected the offending symbol to mention heredoc_body, got %q", budgetErr.Symbol)
		}
		msg := err.Error()
		for _, want := range []string{"synthetic-state", "budget", "heredoc_body", "ruby"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error message %q missing expected substring %q", msg, want)
			}
		}
		t.Logf("got expected budget error: %v", err)
	}

	t.Run("natural size converges (sanity, no override)", func(t *testing.T) {
		ng, tables, ctx := buildBudgetGrammar()
		addNonterminalExtraChains(tables, ng, ctx)
		synthetic := tables.StateCount - tables.ExtraChainStateStart
		if synthetic <= tinyBudget {
			t.Fatalf("expected the statement-embedding extra to exceed the tiny test budget %d, got %d synthetic states", tinyBudget, synthetic)
		}
		// The LALR item-set construction currently converges at 32 states. Keep
		// slack for harmless grammar-normalization changes while preventing the
		// old 339-state merge-history graph (and its real-world unbounded form)
		// from silently returning.
		if synthetic > 64 {
			t.Fatalf("statement-embedding extra expanded to %d synthetic states, want <= 64 after LR(0)-core merging", synthetic)
		}
		t.Logf("natural synthetic-state count: %d (main=%d)", synthetic, tables.ExtraChainStateStart)
	})

	t.Run("tiny budget trips fast with a clear, named panic (raw construction)", func(t *testing.T) {
		t.Setenv(extraChainStateBudgetEnv, "8")
		ng, tables, ctx := buildBudgetGrammar()

		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("expected addNonterminalExtraChains to panic once the synthetic-state budget was exceeded, but it returned normally (states=%d, main=%d)",
					tables.StateCount, tables.ExtraChainStateStart)
			}
			budgetErr, ok := r.(*ExtraChainSyntheticStateBudgetError)
			if !ok {
				// Not our budget error - something else went wrong; re-raise so
				// the test fails loudly with the real panic and stack trace.
				panic(r)
			}
			checkBudgetErr(t, budgetErr, tinyBudget)
		}()

		addNonterminalExtraChains(tables, ng, ctx)
		t.Fatalf("unreachable: addNonterminalExtraChains should have panicked (states=%d, main=%d)", tables.StateCount, tables.ExtraChainStateStart)
	})

	t.Run("guarded wrapper converts the panic to a returned error", func(t *testing.T) {
		t.Setenv(extraChainStateBudgetEnv, "8")
		ng, tables, ctx := buildBudgetGrammar()

		err := addNonterminalExtraChainsGuarded(tables, ng, ctx)
		checkBudgetErr(t, err, tinyBudget)
	})

	t.Run("guarded wrapper re-panics anything that is not the budget error", func(t *testing.T) {
		// A nil NormalizedGrammar makes addNonterminalExtraChains's very first
		// statement (ng.TokenCount()) panic with a nil-pointer dereference -
		// some other failure entirely, nothing to do with the synthetic-state
		// budget. addNonterminalExtraChainsGuarded's recover must not swallow
		// this: it should re-panic it unchanged rather than misreport it as a
		// budget error (or silently return nil).
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected addNonterminalExtraChainsGuarded to re-panic an unrelated failure (nil grammar), but it returned normally")
			}
			if budgetErr, ok := r.(*ExtraChainSyntheticStateBudgetError); ok {
				t.Fatalf("re-panicked value must not be *ExtraChainSyntheticStateBudgetError, got %v - the guard must not misreport an unrelated failure as the budget error", budgetErr)
			}
			t.Logf("guarded wrapper correctly re-panicked a non-budget failure: %v", r)
		}()

		_ = addNonterminalExtraChainsGuarded(&LRTables{}, nil, nil)
		t.Fatal("unreachable: addNonterminalExtraChainsGuarded should have re-panicked the nil-grammar failure")
	})

	t.Run("public API returns the named error instead of panicking", func(t *testing.T) {
		t.Setenv(extraChainStateBudgetEnv, "8")

		t.Run("GenerateLanguage", func(t *testing.T) {
			_, err := GenerateLanguage(newBudgetGrammar())
			checkBudgetErr(t, err, tinyBudget)
		})

		t.Run("Generate", func(t *testing.T) {
			_, err := Generate(newBudgetGrammar())
			checkBudgetErr(t, err, tinyBudget)
		})

		t.Run("GenerateLanguageAndBlob", func(t *testing.T) {
			_, _, err := GenerateLanguageAndBlob(newBudgetGrammar())
			checkBudgetErr(t, err, tinyBudget)
		})
	})
}
