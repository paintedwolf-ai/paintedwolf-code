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

func TestSplitBracesBoundedReturnsDetachedSplitAndRefusesMixedRanges(t *testing.T) {
	for _, fixture := range []struct {
		word   string
		braces int
		err    error
	}{
		{"x{a,b}{1,2}", 2, nil}, {"{01..10..2}", 1, nil}, {"{a..z..0}", 1, nil},
		{"x{a", 0, nil}, {`"{a,b}"`, 0, nil}, {"x{1..2..3..4}", 0, nil},
		{"x{1..a}", 0, syntax.ErrBraceMixedRange}, {"{a,{b..2}}", 0, syntax.ErrBraceMixedRange},
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
		original := len(word.Parts)
		before := pool.UsedBytes()
		split, err := syntax.SplitBracesBounded(word, pool)
		if !errors.Is(err, fixture.err) || (fixture.err == nil && err != nil) {
			t.Fatalf("split %q error = %v; want %v", fixture.word, err, fixture.err)
		}
		if len(word.Parts) != original {
			t.Fatalf("split %q mutated the word", fixture.word)
		}
		if fixture.braces == 0 {
			if split != nil {
				t.Fatalf("split %q = %d parts; want no expansion", fixture.word, len(split.Parts))
			}
			continue
		}
		braces := 0
		for _, part := range split.Parts {
			if _, ok := part.(*syntax.BraceExp); ok {
				braces++
			}
		}
		if braces != fixture.braces || pool.UsedBytes() <= before {
			t.Fatalf("split %q has %d brace parts; want %d charged", fixture.word, braces, fixture.braces)
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

func TestAdmittedLedgerChargesAggregateBeforeGrowth(t *testing.T) {
	var admitted []int64
	refuse := errors.New("aggregate exhausted")
	limit := int64(96 << 10)
	pool, err := syntax.NewAdmittedAllocationBudget(1<<20, func(n int64) error {
		var total int64
		for _, a := range admitted {
			total += a
		}
		if total+n > limit {
			return refuse
		}
		admitted = append(admitted, n)
		return nil
	})
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	if err := pool.ReserveBytes(10, syntax.Pos{}); err != nil || pool.AdmittedBytes() != 64<<10 || len(admitted) != 1 {
		t.Fatalf("first chunk = %v admitted=%d calls=%v", err, pool.AdmittedBytes(), admitted)
	}
	if err := pool.ReserveBytes(64<<10-10, syntax.Pos{}); err != nil || len(admitted) != 1 {
		t.Fatalf("growth within chunk charged again: %v %v", err, admitted)
	}
	var refusal *syntax.BudgetError
	if err := pool.ReserveBytes(64<<10, syntax.Pos{}); !errors.As(err, &refusal) || !errors.Is(err, refuse) || refusal.Kind != "aggregate_admission" || pool.UsedBytes() != 64<<10 {
		t.Fatalf("aggregate refusal = %v used=%d", err, pool.UsedBytes())
	}
	if _, err := syntax.NewParser(syntax.WithBudget(syntax.Budget{Pool: pool})).Parse(strings.NewReader("printf '%s' fixture"), "fixture.pw"); !errors.Is(err, refuse) {
		t.Fatalf("parser hid aggregate refusal: %v", err)
	}
	if _, err := syntax.NewAdmittedAllocationBudget(1, nil); err == nil {
		t.Fatal("admitted ledger accepted no aggregate")
	}
}
