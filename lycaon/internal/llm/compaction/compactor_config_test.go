package compaction

import (
	"testing"
)

func TestDerivedTokenBudgetsFromWindow(t *testing.T) {
	cfg, err := NormalizeConfig(CompactionConfig{
		ModelContextWindow:               200000,
		BudgetTriggerPct:                 70,
		TargetTokensPct:                  40,
		PruneProtectTailMessages:         8,
		ChunkProtectedToolTokenThreshold: 2500,
		ChunkProtectedTailTokenBudget:    25000,
		ChunkProtectedMinSavingsTokens:   300,
		MessageSlice:                     80,
		KeepRecentMessages:               24,
		ChunkTokenThreshold:              2500,
		ChunkMinSavingsTokens:            1000,
		ChunkTargetTokens:                1500,
		ChunkMaxPerPass:                  5,
		MaxWorkerSummaryChars:            16000,
		MaxCitationGroundingRetries:      25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HardCeilingTokens != 170000 {
		t.Fatalf("HardCeilingTokens = %d want 170000 (85%% of 200K)", cfg.HardCeilingTokens)
	}
	if cfg.TargetTokens != 80000 {
		t.Fatalf("TargetTokens = %d want 80000 (40%% of 200K)", cfg.TargetTokens)
	}
}

func TestDerivedTokenBudgets128KWindow(t *testing.T) {
	cfg, err := NormalizeConfig(CompactionConfig{
		ModelContextWindow:               131072,
		BudgetTriggerPct:                 70,
		TargetTokensPct:                  44,
		PruneProtectTailMessages:         8,
		ChunkProtectedToolTokenThreshold: 2500,
		ChunkProtectedTailTokenBudget:    25000,
		ChunkProtectedMinSavingsTokens:   300,
		MessageSlice:                     80,
		KeepRecentMessages:               24,
		ChunkTokenThreshold:              2500,
		ChunkMinSavingsTokens:            1000,
		ChunkTargetTokens:                1500,
		ChunkMaxPerPass:                  5,
		MaxWorkerSummaryChars:            16000,
		MaxCitationGroundingRetries:      25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HardCeilingTokens != 111411 {
		t.Fatalf("HardCeilingTokens = %d want 111411", cfg.HardCeilingTokens)
	}
	if cfg.TargetTokens != 57671 {
		t.Fatalf("TargetTokens = %d want 57671", cfg.TargetTokens)
	}
}

func TestExplicitTokenBudgetsOverrideDerived(t *testing.T) {
	cfg, err := NormalizeConfig(CompactionConfig{
		ModelContextWindow:               200000,
		BudgetTriggerPct:                 70,
		TargetTokensPct:                  40,
		HardCeilingTokens:                120000,
		TargetTokens:                     75000,
		PruneProtectTailMessages:         8,
		ChunkProtectedToolTokenThreshold: 2500,
		ChunkProtectedTailTokenBudget:    25000,
		ChunkProtectedMinSavingsTokens:   300,
		MessageSlice:                     80,
		KeepRecentMessages:               24,
		ChunkTokenThreshold:              2500,
		ChunkMinSavingsTokens:            1000,
		ChunkTargetTokens:                1500,
		ChunkMaxPerPass:                  5,
		MaxWorkerSummaryChars:            16000,
		MaxCitationGroundingRetries:      25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HardCeilingTokens != 120000 {
		t.Fatalf("HardCeilingTokens = %d want explicit 120000", cfg.HardCeilingTokens)
	}
	if cfg.TargetTokens != 75000 {
		t.Fatalf("TargetTokens = %d want explicit 75000", cfg.TargetTokens)
	}
}

func TestCompactionTriggerTokens(t *testing.T) {
	trigger := CompactionConfig{ModelContextWindow: 200000, BudgetTriggerPct: 70}.CompactionTriggerTokens()
	if trigger != 140000 {
		t.Fatalf("CompactionTriggerTokens = %d want 140000 (70%% of 200K)", trigger)
	}
}

func TestCompactionTriggerTokensDefaultsBudgetPct(t *testing.T) {
	trigger := CompactionConfig{ModelContextWindow: 200000}.CompactionTriggerTokens()
	want := 200000 * defaultBudgetTriggerPct / 100
	if trigger != want {
		t.Fatalf("CompactionTriggerTokens = %d want %d (default budget trigger pct)", trigger, want)
	}
}

func TestCompactionTriggerTokensCappedByHardCeiling(t *testing.T) {
	trigger := CompactionConfig{
		ModelContextWindow: 200000,
		BudgetTriggerPct:   70,
		HardCeilingTokens:  120000,
	}.CompactionTriggerTokens()
	if trigger != 120000 {
		t.Fatalf("CompactionTriggerTokens = %d want 120000 (hard ceiling below trigger)", trigger)
	}
}

func TestCompactionTriggerTokensUnknownWindow(t *testing.T) {
	if trigger := (CompactionConfig{}).CompactionTriggerTokens(); trigger != 0 {
		t.Fatalf("CompactionTriggerTokens = %d want 0 for unknown window", trigger)
	}
}

func TestHardCeilingPctOverridesBudgetTriggerForDerivedCeiling(t *testing.T) {
	cfg, err := NormalizeConfig(CompactionConfig{
		ModelContextWindow:               200000,
		BudgetTriggerPct:                 70,
		HardCeilingPct:                   60,
		TargetTokensPct:                  40,
		PruneProtectTailMessages:         8,
		ChunkProtectedToolTokenThreshold: 2500,
		ChunkProtectedTailTokenBudget:    25000,
		ChunkProtectedMinSavingsTokens:   300,
		MessageSlice:                     80,
		KeepRecentMessages:               24,
		ChunkTokenThreshold:              2500,
		ChunkMinSavingsTokens:            1000,
		ChunkTargetTokens:                1500,
		ChunkMaxPerPass:                  5,
		MaxWorkerSummaryChars:            16000,
		MaxCitationGroundingRetries:      25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HardCeilingTokens != 140000 {
		t.Fatalf("HardCeilingTokens = %d want 140000 (60%% pct clamped up to 70%% trigger)", cfg.HardCeilingTokens)
	}
}

func TestLoadBundledCompactionConfigDerivesBudgets(t *testing.T) {
	cfg := DefaultCompactionConfig()
	// Bundled config is a placeholder live budget (131072).
	if cfg.ModelContextWindow != 131072 {
		t.Fatalf("ModelContextWindow = %d want 131072", cfg.ModelContextWindow)
	}
	if cfg.HardCeilingTokens != 131072*85/100 {
		t.Fatalf("HardCeilingTokens = %d want %d", cfg.HardCeilingTokens, 131072*85/100)
	}
	if cfg.TargetTokens != 131072*35/100 {
		t.Fatalf("TargetTokens = %d want %d", cfg.TargetTokens, 131072*35/100)
	}
}

func TestCompactionPctKnobsDerivedTokens(t *testing.T) {
	cfg := DefaultCompactionConfig()
	if cfg.BudgetTriggerPct != 60 {
		t.Fatalf("BudgetTriggerPct = %d want 60", cfg.BudgetTriggerPct)
	}
	if cfg.TargetTokensPct != 35 {
		t.Fatalf("TargetTokensPct = %d want 35", cfg.TargetTokensPct)
	}
	if cfg.KeepRecentMessages != 16 {
		t.Fatalf("KeepRecentMessages = %d want 16", cfg.KeepRecentMessages)
	}
	if cfg.ChunkTokenThreshold != 1800 {
		t.Fatalf("ChunkTokenThreshold = %d want 1800", cfg.ChunkTokenThreshold)
	}
	if cfg.ChunkProtectedToolTokenThreshold != 1800 {
		t.Fatalf("ChunkProtectedToolTokenThreshold = %d want 1800", cfg.ChunkProtectedToolTokenThreshold)
	}
	if cfg.ChunkProtectedTailTokenBudget != 16000 {
		t.Fatalf("ChunkProtectedTailTokenBudget = %d want 16000", cfg.ChunkProtectedTailTokenBudget)
	}
	if cfg.ChunkMaxPerPass != 8 {
		t.Fatalf("ChunkMaxPerPass = %d want 8", cfg.ChunkMaxPerPass)
	}
	if cfg.ChunkMinSavingsTokens != 500 {
		t.Fatalf("ChunkMinSavingsTokens = %d want 500", cfg.ChunkMinSavingsTokens)
	}
	if cfg.ChunkTargetTokens != 1000 {
		t.Fatalf("ChunkTargetTokens = %d want 1000", cfg.ChunkTargetTokens)
	}
	if cfg.PruneProtectTailMessages != 8 {
		t.Fatalf("PruneProtectTailMessages = %d want 8", cfg.PruneProtectTailMessages)
	}
	if got := cfg.CompactionTriggerTokens(); got != 78643 {
		t.Fatalf("CompactionTriggerTokens = %d want 78643 (131072×60/100)", got)
	}
	if cfg.TargetTokens != 45875 {
		t.Fatalf("TargetTokens = %d want 45875 (131072×35/100)", cfg.TargetTokens)
	}
	if cfg.HardCeilingTokens != 111411 {
		t.Fatalf("HardCeilingTokens = %d want 111411 (131072×85/100)", cfg.HardCeilingTokens)
	}
}

func TestDefaultCompactionConfigUsesYAMLWindow(t *testing.T) {
	cfg := DefaultCompactionConfig()
	if cfg.ModelContextWindow != 131072 {
		t.Fatalf("DefaultCompactionConfig ModelContextWindow = %d want 131072 (YAML SSOT, not Go 200000 twin)", cfg.ModelContextWindow)
	}
}
