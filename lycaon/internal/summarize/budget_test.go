package summarize

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// EstimatePackTokens measures pack sections charged to the input budget.
func (c Caps) EstimatePackTokens(pack ContextPack) int {
	var total int
	for _, id := range pack.Identity {
		total += c.EstimateTokens(identityLine(id))
	}
	for _, s := range pack.Skeleton {
		total += c.EstimateTokens(fmt.Sprintf("%s %s %s %d", s.Path, s.Kind, s.Name, s.Line))
	}
	for _, w := range pack.Substance {
		total += c.EstimateTokens(windowLine(w))
	}
	for _, e := range pack.Imports {
		total += c.EstimateTokens(importEdgeLine(e))
	}
	for _, n := range pack.Neighbors {
		total += c.EstimateTokens(neighborLine(n))
	}
	for _, cs := range pack.CallSites {
		total += c.EstimateTokens(callSiteLine(cs))
	}
	return total
}

func TestEstimateTokens(t *testing.T) {
	caps := DefaultCaps()
	if caps.Pack.SizeDivisor != 4 {
		t.Fatalf("SizeDivisor = %d want 4", caps.Pack.SizeDivisor)
	}
	got := caps.EstimateTokens("abcd") // 4 runes / 4 = 1
	if got != 1 {
		t.Fatalf("EstimateTokens(abcd) = %d want 1", got)
	}
	got = caps.EstimateTokens("abcdefgh") // 8/4 = 2
	if got != 2 {
		t.Fatalf("EstimateTokens(abcdefgh) = %d want 2", got)
	}
	got = caps.EstimateTokens("ab") // 2 runes → ceil → 1 (never 0 for non-empty)
	if got != 1 {
		t.Fatalf("EstimateTokens(ab) = %d want 1", got)
	}
	if caps.EstimateTokens("") != 0 {
		t.Fatal("empty string must estimate 0")
	}
}

func TestAssemblePackUnderBudget(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 80
	caps.Pack.SizeDivisor = 1
	caps.Pack.BreadthFractionPct = 50
	ranked := []StructureCandidate{
		{RelPath: "a.go", Kind: StructureKindFile, LineCount: 4, StartLine: 1,
			Head: "aaaa", Symbols: []StructureSymbol{{Kind: "func", Name: "A", Line: 1}}},
		{RelPath: "b.go", Kind: StructureKindFile, LineCount: 80, StartLine: 1,
			Head: strings.Repeat("b", 80), Symbols: []StructureSymbol{{Kind: "func", Name: "B", Line: 1}}},
		{RelPath: "c.go", Kind: StructureKindFile, LineCount: 4, StartLine: 1,
			Head: "cccc", Symbols: []StructureSymbol{{Kind: "func", Name: "C", Line: 1}}},
	}
	pack, stats, leftover := assembleContextPack(context.Background(), noRerank, "task", ranked, caps, FitEdges{})
	if stats.PrimaryLimitReason != "pack_budget" && len(pack.Gaps) == 0 && len(leftover) == 0 {
		t.Fatalf("expected budget truncation; stats=%+v gaps=%v leftover=%v", stats, pack.Gaps, leftover)
	}
	if got := caps.EstimatePackTokens(pack); got > caps.Pack.InputBudgetTokens {
		t.Fatalf("admitted pack %d exceeds budget %d", got, caps.Pack.InputBudgetTokens)
	}
}
