package syntax_test

import (
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

const scratchCeiling = 32 << 20

func parseBudget(source string, maximum int64) (*syntax.File, error) {
	return syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.WithBudget(syntax.Budget{MaxBytes: maximum})).Parse(strings.NewReader(source), "fixture.pw")
}
func requireBudgetRefusal(t *testing.T, source string, maximum int64) {
	t.Helper()
	file, err := parseBudget(source, maximum)
	var refusal *syntax.BudgetError
	if file != nil || !errors.As(err, &refusal) || refusal.Kind != "parser_scratch_bytes" || refusal.Maximum != maximum || refusal.Observed <= maximum {
		t.Fatalf("expected typed scratch refusal without tree, got tree=%t error=%v", file != nil, err)
	}
}
func TestBudgetExactReservationBoundary(t *testing.T) {
	low, high := int64(0), int64(scratchCeiling)
	for low < high {
		middle := low + (high-low)/2
		_, err := parseBudget("printf '%s\\n' value", middle)
		if err == nil {
			high = middle
		} else {
			low = middle + 1
		}
	}
	if _, err := parseBudget("printf '%s\\n' value", low); err != nil {
		t.Fatalf("at reservation boundary: %v", err)
	}
	requireBudgetRefusal(t, "printf '%s\\n' value", low-1)
	requireBudgetRefusal(t, "true", 0)
	requireBudgetRefusal(t, "true", -1)
}
func TestBudgetValidSelectedTreeBoundariesRemainAccepted(t *testing.T) {
	sources := []struct {
		name, source string
		nodes, depth int
	}{
		{"nodes8192", ": >x;" + strings.Repeat(":;", 2046), 8192, 4},
		{"depth64", strings.Repeat("( ", 30) + ":" + strings.Repeat(" )", 30), 0, 64},
		{"source1MiB", "printf '" + strings.Repeat("x", (1<<20)-9) + "'", 0, 4},
	}
	for _, fixture := range sources {
		t.Run(fixture.name, func(t *testing.T) {
			file, err := parseBudget(fixture.source, scratchCeiling)
			if err != nil {
				t.Fatalf("valid selected tree refused: %v", err)
			}
			count, depth, maximum := 0, -1, 0
			syntax.Walk(file, func(node syntax.Node) bool {
				if node == nil {
					depth--
					return true
				}
				count++
				depth++
				if depth > maximum {
					maximum = depth
				}
				return true
			})
			if fixture.nodes > 0 && count != fixture.nodes {
				t.Fatalf("expected %d nodes, got %d", fixture.nodes, count)
			}
			if maximum != fixture.depth {
				t.Fatalf("expected depth %d, got %d", fixture.depth, maximum)
			}
		})
	}
}
func TestBudgetDenseAndMalformedDeepInputRefusesBeforeUnboundedGrowth(t *testing.T) {
	for _, source := range []string{
		strings.Repeat("x ", 1<<19),
		strings.Repeat("(", 20000) + ":" + strings.Repeat(")", 20000),
		strings.Repeat("(", 20000),
		"echo $((" + strings.Repeat("(", 20000) + "1",
		"echo " + strings.Repeat("$(", 10000),
	} {
		requireBudgetRefusal(t, source, scratchCeiling)
	}
}
func TestBudgetParserReuseAfterAbortAndOrdinarySyntaxError(t *testing.T) {
	parser := syntax.NewParser(syntax.WithBudget(syntax.Budget{MaxBytes: scratchCeiling}))
	file, err := parser.Parse(strings.NewReader(strings.Repeat("(", 20000)), "malformed.pw")
	var refusal *syntax.BudgetError
	if file != nil || !errors.As(err, &refusal) {
		t.Fatalf("deep refusal: %v", err)
	}
	if _, err := parser.Parse(strings.NewReader("if"), "bad.pw"); err == nil {
		t.Fatal("expected ordinary syntax error")
	}
	if file, err := parser.Parse(strings.NewReader("printf done"), "good.pw"); err != nil || file == nil {
		t.Fatalf("reuse after refusal: %v", err)
	}
}
func TestBudgetOnlyParseEntryIsSupported(t *testing.T) {
	parser := syntax.NewParser(syntax.WithBudget(syntax.Budget{MaxBytes: scratchCeiling}))
	assert := func(err error) {
		t.Helper()
		var refusal *syntax.BudgetError
		if !errors.As(err, &refusal) || refusal.Kind != "parser_entry_unsupported" {
			t.Fatalf("expected explicit unsupported entry, got %v", err)
		}
	}
	_, err := parser.Document(strings.NewReader("value"))
	assert(err)
	_, err = parser.Arithmetic(strings.NewReader("1+2"))
	assert(err)
	for file, err := range parser.StmtsSeq(strings.NewReader("true")) {
		if file != nil {
			t.Fatal("unsupported entry yielded a tree")
		}
		assert(err)
	}
	for word, err := range parser.WordsSeq(strings.NewReader("value")) {
		if word != nil {
			t.Fatal("unsupported entry yielded a word")
		}
		assert(err)
	}
}
func TestBudgetDoesNotRecoverUnrelatedReaderPanics(t *testing.T) {
	defer func() {
		if recover() != "fixture-reader-panic" {
			t.Fatal("unrelated panic must propagate")
		}
	}()
	parser := syntax.NewParser(syntax.WithBudget(syntax.Budget{MaxBytes: scratchCeiling}))
	parser.Parse(panicReader{}, "panic.pw")
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("fixture-reader-panic") }
