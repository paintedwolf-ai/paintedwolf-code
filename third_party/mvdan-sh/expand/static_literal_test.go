package expand_test

import (
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func staticWord(t *testing.T, source string, pool *syntax.AllocationBudget) (*syntax.Word, *syntax.Parser) {
	t.Helper()
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.WithBudget(syntax.Budget{Pool: pool}))
	file, err := parser.Parse(strings.NewReader(": "+source), "fixture.pw")
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return file.Stmts[0].Cmd.(*syntax.CallExpr).Args[1], parser
}

func allocationPool(t *testing.T, maximum int64) *syntax.AllocationBudget {
	t.Helper()
	pool, err := syntax.NewAllocationBudget(maximum)
	if err != nil {
		t.Fatalf("allocate fixture ledger: %v", err)
	}
	return pool
}

func TestStaticLiteralSelectedQuoteRemoval(t *testing.T) {
	for _, fixture := range []struct {
		source, value string
		static        bool
	}{
		{`e\val`, "eval", true}, {`'ev'"al"`, "eval", true},
		{`"e\val"`, `e\val`, true}, {`"e\\val"`, `e\val`, true},
		{`\世'界'`, "世界", true}, {"e\\\nval", "eval", true},
		{`"\$name"`, "$name", true}, {`'a\b'`, `a\b`, true},
		{`''`, "", true}, {`''~`, "~", true}, {`'~user'`, "~user", true},
		{`~user`, "", false}, {`\~user`, "~user", true},
		{`\{a,b\}`, "{a,b}", true}, {`"{a,b}"`, "{a,b}", true},
		{`x{a,b}`, "", false}, {`x{1..3}`, "", false},
		{`x{a`, "x{a", true}, {`x{1..a}`, "x{1..a}", true},
		{`$'eval'`, "", false}, {`${name}`, "", false},
		{`"$name"`, "", false}, {`$(false)`, "", false},
	} {
		t.Run(fixture.source, func(t *testing.T) {
			pool := allocationPool(t, 32<<20)
			word, _ := staticWord(t, fixture.source, pool)
			value, static, err := expand.StaticLiteral(word, pool)
			if err != nil || static != fixture.static || value != fixture.value {
				t.Fatalf("static literal = %q,%v,%v; want %q,%v", value, static, err, fixture.value, fixture.static)
			}
		})
	}
}

func TestStaticLiteralEscapesDoNotAmplifyIntoFieldParts(t *testing.T) {
	pool := allocationPool(t, 32<<20)
	word, parser := staticWord(t, strings.Repeat(`\x`, (1<<19)-1), pool)
	parsed := pool.UsedBytes()
	if parsed != parser.ScratchBytes() {
		t.Fatalf("parser usage = %d, ledger = %d", parser.ScratchBytes(), parsed)
	}
	value, static, err := expand.StaticLiteral(word, pool)
	if err != nil || !static || value != strings.Repeat("x", (1<<19)-1) {
		t.Fatalf("large escaped word static=%v error=%v bytes=%d", static, err, len(value))
	}
	if extra := pool.UsedBytes() - parsed; extra > 2<<20 {
		t.Fatalf("quote removal amplified one source word into %d scratch bytes", extra)
	}
}

func TestStaticLiteralSharedLedgerAtLimitAndOneByteOver(t *testing.T) {
	const source = `e\v'a'"l"`
	run := func(maximum int64) (int64, error) {
		pool := allocationPool(t, maximum)
		word, _ := staticWord(t, source, pool)
		if _, static, err := expand.StaticLiteral(word, pool); err != nil || !static {
			return pool.UsedBytes(), err
		}
		err := pool.ReserveHeap(1<<20, word.Pos()) // caller's host lowering
		return pool.UsedBytes(), err
	}
	maximum, err := run(32 << 20)
	if err != nil {
		t.Fatalf("measure shared fixture: %v", err)
	}
	if got, err := run(maximum); err != nil || got != maximum {
		t.Fatalf("at-limit aggregate=%d error=%v", got, err)
	}
	_, err = run(maximum - 1)
	var refusal *syntax.BudgetError
	if !errors.As(err, &refusal) || refusal.Maximum != maximum-1 || refusal.Observed != maximum {
		t.Fatalf("over-limit shared lowering error = %v", err)
	}
}

func TestStaticLiteralBudgetRejectsMalformedBraceAmplification(t *testing.T) {
	pool := allocationPool(t, 32<<20)
	word, _ := staticWord(t, "x"+strings.Repeat("{", (1<<20)-3), pool)
	_, static, err := expand.StaticLiteral(word, pool)
	var refusal *syntax.BudgetError
	if static || !errors.As(err, &refusal) || refusal.Observed <= refusal.Maximum {
		t.Fatalf("brace amplification static=%v error=%v", static, err)
	}
	if pool.UsedBytes() > pool.MaximumBytes() {
		t.Fatal("refused allocation changed ledger beyond its ceiling")
	}
}

func TestStaticLiteralDoesNotBorrowMutableNodeStorage(t *testing.T) {
	pool := allocationPool(t, 32<<20)
	word, _ := staticWord(t, `e\val`, pool)
	value, static, err := expand.StaticLiteral(word, pool)
	if err != nil || !static {
		t.Fatalf("capture literal: %v", err)
	}
	word.Parts[0].(*syntax.Lit).Value = "changed"
	if value != "eval" {
		t.Fatalf("retained literal changed: %q", value)
	}
}
