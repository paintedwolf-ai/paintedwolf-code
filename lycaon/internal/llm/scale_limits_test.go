package llm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestScaleLimitsFromTrueWindowKimi(t *testing.T) {
	got := ScaleLimitsFromTrueWindow(262144)
	if got.MaxToolResultBytes != 524288 {
		t.Fatalf("MaxToolResultBytes = %d want 524288", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("MaxCoordinatorLoopCycles = %d want 256", got.MaxCoordinatorLoopCycles)
	}
	if got.MaxWorkerSummaryChars != 16384 {
		t.Fatalf("MaxWorkerSummaryChars = %d want 16384", got.MaxWorkerSummaryChars)
	}
}

func TestScaleLimitsFromTrueWindow32k(t *testing.T) {
	got := ScaleLimitsFromTrueWindow(32768)
	if got.MaxToolResultBytes != 65536 {
		t.Fatalf("MaxToolResultBytes = %d want 65536", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 32 {
		t.Fatalf("MaxCoordinatorLoopCycles = %d want 32", got.MaxCoordinatorLoopCycles)
	}
	if got.MaxWorkerSummaryChars != 2048 {
		t.Fatalf("MaxWorkerSummaryChars = %d want 2048", got.MaxWorkerSummaryChars)
	}
}

func TestScaleLimitsFromTrueWindowTiny(t *testing.T) {
	got := ScaleLimitsFromTrueWindow(100)
	if got.MaxToolResultBytes != 200 {
		t.Fatalf("MaxToolResultBytes = %d want 200", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 1 {
		t.Fatalf("MaxCoordinatorLoopCycles = %d want 1", got.MaxCoordinatorLoopCycles)
	}
	if got.MaxWorkerSummaryChars != 6 {
		t.Fatalf("MaxWorkerSummaryChars = %d want 6", got.MaxWorkerSummaryChars)
	}
}

func TestNoTierSymbols(t *testing.T) {
	// Build banned substrings without embedding them contiguous in this file.
	banned := []string{
		"context" + "_" + "tier",
		"Context" + "Budget" + "Tier",
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		for _, sym := range banned {
			if strings.Contains(text, sym) {
				t.Errorf("%s contains banned tier symbol %q", path, sym)
			}
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			// Named 32k tier id types / consts are forbidden.
			if ident.Name == "Tier"+"32k" || ident.Name == "ContextTier"+"32k" {
				t.Errorf("%s declares banned tier id %q", path, ident.Name)
			}
			return true
		})
		return nil
	})
	testutil.FailErr(t, "walk Go sources", err)
}

func TestNormalizeLiveBudgetPctAndWindowSource(t *testing.T) {
	cfg, err := compaction.NormalizeConfig(compaction.CompactionConfig{
		ModelContextWindow:               131072,
		PruneProtectTailMessages:         8,
		ChunkProtectedToolTokenThreshold: 1800,
		ChunkProtectedTailTokenBudget:    16000,
		ChunkProtectedMinSavingsTokens:   300,
		MessageSlice:                     80,
		KeepRecentMessages:               16,
		ChunkTokenThreshold:              1800,
		ChunkMinSavingsTokens:            500,
		ChunkTargetTokens:                1000,
		ChunkMaxPerPass:                  8,
		MaxCitationGroundingRetries:      25,
	})
	testutil.FailErr(t, "normalize compaction config", err)
	if cfg.LiveBudgetPct != compaction.DefaultLiveBudgetPct {
		t.Fatalf("LiveBudgetPct = %d want %d", cfg.LiveBudgetPct, compaction.DefaultLiveBudgetPct)
	}
	if cfg.WindowSource != compaction.WindowSourceAuto {
		t.Fatalf("WindowSource = %q want %q", cfg.WindowSource, compaction.WindowSourceAuto)
	}
}

func TestNormalizeRejectsUnknownWindowSource(t *testing.T) {
	_, err := compaction.NormalizeConfig(compaction.CompactionConfig{
		ModelContextWindow:               131072,
		WindowSource:                     "tier",
		PruneProtectTailMessages:         8,
		ChunkProtectedToolTokenThreshold: 1800,
		ChunkProtectedTailTokenBudget:    16000,
		ChunkProtectedMinSavingsTokens:   300,
		MessageSlice:                     80,
		KeepRecentMessages:               16,
		ChunkTokenThreshold:              1800,
		ChunkMinSavingsTokens:            500,
		ChunkTargetTokens:                1000,
		ChunkMaxPerPass:                  8,
		MaxCitationGroundingRetries:      25,
	})
	if err == nil {
		t.Fatal("expected error for unknown window_source")
	}
}

func TestLoadBundledCompactionHasLiveBudgetKnobs(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	if cfg.LiveBudgetPct != 75 {
		t.Fatalf("LiveBudgetPct = %d want 75", cfg.LiveBudgetPct)
	}
	if cfg.WindowSource != compaction.WindowSourceAuto {
		t.Fatalf("WindowSource = %q want %q", cfg.WindowSource, compaction.WindowSourceAuto)
	}
}
