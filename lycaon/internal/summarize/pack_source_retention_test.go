package summarize

import "testing"

func TestTrimPackKeepsCoverageUnderSeverePressure(t *testing.T) {
	pack := ContextPack{
		Skeleton:  []PackSymbol{{Path: "other", Kind: KindDirectoryRollup, Name: "12 files"}},
		Substance: []PackWindow{{Path: "route.go", StartLine: 10, EndLine: 12, Body: "10: func Route() int {\n11: return 1\n12: }"}},
	}
	if !TrimPackOneStep("route", &pack) || len(pack.Substance) != 0 || len(pack.Skeleton) != 1 {
		t.Fatalf("coverage must outlive source when neither fits: %+v", pack)
	}
}

func TestTrimPackPreservesSourceBeforeUnbackedNames(t *testing.T) {
	pack := ContextPack{
		Skeleton: []PackSymbol{
			{Path: "route.go", Kind: "func", Name: "Route", Line: 10},
			{Path: "route.go", Kind: "func", Name: "UnusedRouteName", Line: 90},
			{Path: "other", Kind: KindDirectoryRollup, Name: "12 files"},
		},
		Substance: []PackWindow{{Path: "route.go", StartLine: 10, EndLine: 12, Symbol: "Route", Body: "10: func Route() int {\n11: return 1\n12: }"}},
	}
	if !TrimPackOneStep("route", &pack) {
		t.Fatal("expected trim step")
	}
	if len(pack.Substance) != 1 || len(pack.Skeleton) != 2 || pack.Skeleton[0].Name != "Route" {
		t.Fatalf("source-backed anchor lost before optional name: %+v", pack)
	}
}
