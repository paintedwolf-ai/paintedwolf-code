package summarize

import (
	"strings"
	"testing"
)

func TestTrimPackOneStepDropsLowTaskScoreSubstance(t *testing.T) {
	pack := ContextPack{
		Substance: []PackWindow{
			{Path: "pkg/auth/verify.go", Symbol: "VerifyToken", Body: strings.Repeat("a", 400)},
			{Path: "pkg/logging/trace.go", Symbol: "TraceEvent", Body: strings.Repeat("b", 400)},
		},
	}
	if !TrimPackOneStep("how does auth verify tokens", &pack) {
		t.Fatal("expected trim step")
	}
	if len(pack.Substance) != 1 {
		t.Fatalf("substance len = %d want 1", len(pack.Substance))
	}
	if pack.Substance[0].Symbol != "VerifyToken" {
		t.Fatalf("kept %q want VerifyToken (task match)", pack.Substance[0].Symbol)
	}
}

func TestTrimPackOneStepProtectsRollupsBeforeSymbols(t *testing.T) {
	pack := ContextPack{
		Skeleton: []PackSymbol{
			{Path: "pkg/core", Kind: "function", Name: "Dispatch", Line: 10},
			{Path: "pkg/other", Kind: KindDirectoryRollup, Name: "12 files", Line: 1},
			{Path: "pkg/logging", Kind: "function", Name: "Trace", Line: 3},
		},
	}
	if !TrimPackOneStep("dispatch pipeline", &pack) {
		t.Fatal("expected trim step")
	}
	for _, s := range pack.Skeleton {
		if s.Kind == KindDirectoryRollup {
			return
		}
	}
	t.Fatal("directory_rollup row should survive before other symbol rows are gone")
}

func TestTrimPackOneStepDropsIdentityTailAsLastResort(t *testing.T) {
	pack := ContextPack{
		Identity: []PackIdentity{
			{Path: "pkg/auth/verify.go"},
			{Path: "pkg/logging/trace.go"},
			{Path: "pkg/util/misc.go"},
		},
	}
	// No fit/substance/skeleton — identity is the only trimmable tier.
	if !TrimPackOneStep("how does auth verify", &pack) {
		t.Fatal("expected identity trim step")
	}
	if len(pack.Identity) != 2 {
		t.Fatalf("identity len = %d want 2", len(pack.Identity))
	}
	// Highest task affinity must survive the tail drop.
	if pack.Identity[0].Path != "pkg/auth/verify.go" {
		t.Fatalf("dropped the task-relevant identity: %+v", pack.Identity)
	}
	// A running omission note is recorded.
	gapNote := false
	for _, g := range pack.Gaps {
		if strings.HasPrefix(g, identityTrimGapPrefix) {
			gapNote = true
		}
	}
	if !gapNote {
		t.Fatalf("expected identity-trim gap note, gaps=%v", pack.Gaps)
	}
	// Never drops below the last row (keeps ≥1 for orientation).
	for i := 0; i < 5; i++ {
		TrimPackOneStep("how does auth verify", &pack)
	}
	if len(pack.Identity) != 1 {
		t.Fatalf("identity floor = %d want 1", len(pack.Identity))
	}
}

func TestTrimPackOneStepDropsFitEdgesFirst(t *testing.T) {
	pack := ContextPack{
		Imports: []PackImportEdge{{From: "a.go", To: "b", Kind: "import"}},
		Substance: []PackWindow{
			{Path: "a.go", Symbol: "Main", Body: "x"},
		},
	}
	if !TrimPackOneStep("anything", &pack) {
		t.Fatal("expected trim")
	}
	if len(pack.Imports) != 0 {
		t.Fatal("imports should drop before substance")
	}
	if len(pack.Substance) != 1 {
		t.Fatal("substance should remain after fit trim")
	}
}
