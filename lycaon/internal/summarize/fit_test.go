package summarize

import (
	"context"
	"strings"
	"testing"
)

func TestFitDropsFirstUnderBudget(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 90
	caps.Pack.SizeDivisor = 1
	caps.Pack.BreadthFractionPct = 60
	caps.Gather.SymbolWindowLines = 6

	structure := []StructureCandidate{
		{
			RelPath: "pkg/core.go", Kind: StructureKindFile, LineCount: 20, StartLine: 1,
			Head:    "package pkg\nfunc Core() {\n\tx := 1\n\ty := 2\n\treturn\n}\n",
			Symbols: []StructureSymbol{{Kind: "func", Name: "Core", Line: 2}},
		},
	}
	// Expensive fit garnish — should lose to substance under a tight budget.
	fit := FitEdges{
		Neighbors: []PackNeighbor{
			{Path: "cmd/main.go", Why: strings.Repeat("ref ", 40)},
			{Path: "other/use.go", Why: strings.Repeat("ref ", 40)},
		},
		CallSites: []PackCallSite{
			{Path: "cmd/main.go", Line: 4, Excerpt: strings.Repeat("Core() ", 30)},
			{Path: "other/use.go", Line: 8, Excerpt: strings.Repeat("Core() ", 30)},
		},
	}
	pack, stats, _ := assembleContextPack(context.Background(), noRerank, "Core", structure, caps, fit)
	if len(pack.Skeleton) == 0 {
		t.Fatalf("expected skeleton to win budget; pack=%+v stats=%+v", pack, stats)
	}
	if len(pack.Neighbors) != 0 || len(pack.CallSites) != 0 {
		t.Fatalf("fit must drop first under tight budget; neighbors=%v call_sites=%v spent=%d budget=%d",
			pack.Neighbors, pack.CallSites, stats.BudgetTokensSpent, caps.Pack.InputBudgetTokens)
	}
	if got := caps.EstimatePackTokens(pack); got > caps.Pack.InputBudgetTokens {
		t.Fatalf("pack %d exceeds budget %d", got, caps.Pack.InputBudgetTokens)
	}
}

func TestFitAdmitsInSlack(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 2000
	caps.Pack.SizeDivisor = 4
	caps.Pack.BreadthFractionPct = 50
	structure := []StructureCandidate{
		{
			RelPath: "pkg/core.go", Kind: StructureKindFile, LineCount: 10, StartLine: 1,
			Language: "go", OutlineSource: "tree_sitter",
			Head:    "package pkg\nfunc Core() {}\n",
			Symbols: []StructureSymbol{{Kind: "func", Name: "Core", Line: 2}},
		},
	}
	fit := FitEdges{
		Neighbors: []PackNeighbor{{Path: "cmd/main.go", Why: "ref x1"}},
		CallSites: []PackCallSite{{Path: "cmd/main.go", Line: 4, Excerpt: "Core()"}},
	}
	pack, stats, _ := assembleContextPack(context.Background(), noRerank, "Core", structure, caps, fit)
	if len(pack.Neighbors) != 1 || len(pack.CallSites) != 1 {
		t.Fatalf("expected fit admits; neighbors=%v call_sites=%v stats=%+v", pack.Neighbors, pack.CallSites, stats)
	}
	if stats.FitAdmits < 2 {
		t.Fatalf("FitAdmits = %d want ≥2", stats.FitAdmits)
	}
	if len(pack.Identity) != 1 || pack.Identity[0].Language != "go" {
		t.Fatalf("identity metadata missing: %+v", pack.Identity)
	}
}

func TestFitOneCall(t *testing.T) {
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo,
		Structure: []StructureCandidate{{
			RelPath: "pkg/a.go", Kind: StructureKindFile, LineCount: 5, StartLine: 1,
			Head:    "package pkg\nfunc A() {}\n",
			Symbols: []StructureSymbol{{Kind: "func", Name: "A", Line: 2}},
		}},
		Fit: FitEdges{
			Neighbors: []PackNeighbor{{Path: "cmd/main.go", Why: "ref x1"}},
			CallSites: []PackCallSite{{Path: "cmd/main.go", Line: 3, Excerpt: "A()"}},
		},
		Stats: GatherStats{Mode: ModeRepo, Candidates: 1, PathIsFile: true, UseStructure: true},
	}}, DefaultCaps())
	res, err := eng.Run(context.Background(), Request{Task: "A", Path: "pkg/a.go", MaxAnchors: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Pack.Neighbors) == 0 {
		t.Fatalf("expected fit neighbors in pack; pack=%+v", res.Pack)
	}
}
