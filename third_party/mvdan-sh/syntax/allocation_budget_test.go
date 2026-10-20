package syntax_test

import (
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestParserLegacyBudgetReusesIndependentReservation(t *testing.T) {
	parser := syntax.NewParser(syntax.WithBudget(syntax.Budget{MaxBytes: scratchCeiling}))
	var previous int64
	for i := 0; i < 3; i++ {
		if _, err := parser.Parse(strings.NewReader("printf '%s' fixture"), "fixture.pw"); err != nil {
			t.Fatalf("parse %d: %v", i, err)
		}
		used := parser.ScratchBytes()
		if used <= 0 || (previous != 0 && used != previous) {
			t.Fatalf("independent parser scratch = %d, previous=%d", used, previous)
		}
		previous = used
	}
}

func TestClassifyBracesMatchesSelectedExpansionWithoutMutation(t *testing.T) {
	for _, fixture := range []struct {
		word string
		want bool
	}{
		{"x{a,b}", true}, {"x{1..3}", true}, {"x{1..a}", false},
		{"x{1..2..3..4}", false}, {"x{a", false}, {`x\{a,b\}`, false},
		{`"{a,b}"`, false}, {"x{{a,b}", true},
	} {
		pool, err := syntax.NewAllocationBudget(scratchCeiling)
		if err != nil {
			t.Fatalf("create ledger: %v", err)
		}
		file, err := syntax.NewParser(syntax.WithBudget(syntax.Budget{Pool: pool})).Parse(strings.NewReader(": "+fixture.word), "fixture.pw")
		if err != nil {
			t.Fatalf("parse %q: %v", fixture.word, err)
		}
		word := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
		var before strings.Builder
		if err := syntax.NewPrinter().Print(&before, word); err != nil {
			t.Fatalf("print original word: %v", err)
		}
		got, err := syntax.ClassifyBraces(word, pool)
		if err != nil || got != fixture.want {
			t.Fatalf("classify %q = %v,%v; want %v", fixture.word, got, err, fixture.want)
		}
		var after strings.Builder
		if err := syntax.NewPrinter().Print(&after, word); err != nil || after.String() != before.String() {
			t.Fatalf("classification mutated word: %q => %q (%v)", before.String(), after.String(), err)
		}
	}
}

func TestSharedLedgerRefusesBeforeAllocationAndRetainsUsage(t *testing.T) {
	pool, err := syntax.NewAllocationBudget(160)
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	if err := pool.ReserveHeap(1, syntax.Pos{}); err != nil { // rounded16+64
		t.Fatalf("first allocation: %v", err)
	}
	if err := pool.ReserveHeap(1, syntax.Pos{}); err != nil {
		t.Fatalf("at-limit allocation: %v", err)
	}
	var refusal *syntax.BudgetError
	if err := pool.ReserveBytes(1, syntax.Pos{}); !errors.As(err, &refusal) || refusal.Observed != 161 || pool.UsedBytes() != 160 {
		t.Fatalf("one-byte excess = %v; retained=%d", err, pool.UsedBytes())
	}
}
