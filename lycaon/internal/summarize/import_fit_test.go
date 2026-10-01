package summarize

import (
	"context"
	"strings"
	"testing"
)

func TestImportFitDropsFirst(t *testing.T) {
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
	fit := FitEdges{
		Imports: []PackImportEdge{
			{From: "pkg/core.go", To: strings.Repeat("mod/", 20) + "dep", Kind: "outbound"},
			{From: "cmd/main.go", To: "example.com/app/pkg", Kind: "inbound"},
		},
	}
	pack, stats, _ := assembleContextPack(context.Background(), noRerank, "Core", structure, caps, fit)
	if len(pack.Skeleton) == 0 {
		t.Fatalf("expected skeleton to win budget; pack=%+v stats=%+v", pack, stats)
	}
	if len(pack.Imports) != 0 {
		t.Fatalf("imports must drop first under tight budget; imports=%v spent=%d", pack.Imports, stats.BudgetTokensSpent)
	}
}

func TestImportOneCall(t *testing.T) {
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo,
		Structure: []StructureCandidate{{
			RelPath: "pkg/a.go", Kind: StructureKindFile, LineCount: 5, StartLine: 1,
			Head:    "package pkg\nfunc A() {}\n",
			Symbols: []StructureSymbol{{Kind: "func", Name: "A", Line: 2}},
		}},
		Fit: FitEdges{
			Imports: []PackImportEdge{
				{From: "pkg/a.go", To: "fmt", Kind: "outbound"},
				{From: "cmd/main.go", To: "example.com/app/pkg", Kind: "inbound"},
			},
		},
		Stats: GatherStats{Mode: ModeRepo, Candidates: 1, PathIsFile: true, UseStructure: true},
	}}, DefaultCaps())
	res, err := eng.Run(context.Background(), Request{Task: "A", Path: "pkg/a.go", MaxAnchors: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Pack.Imports) == 0 {
		t.Fatalf("expected imports in pack; pack=%+v", res.Pack)
	}
}
