package settings_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSessionLimitsKimiDerived(t *testing.T) {
	derived := llm.ScaleLimitsFromTrueWindow(262144)
	got := settings.ApplyDerivedSessionLimits(settings.SessionLimits{}, derived)
	if got.MaxToolResultBytes != 524288 {
		t.Fatalf("MaxToolResultBytes = %d want 524288", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("MaxCoordinatorLoopCycles = %d want 256", got.MaxCoordinatorLoopCycles)
	}
	if derived.MaxWorkerSummaryChars != 16384 {
		t.Fatalf("MaxWorkerSummaryChars = %d want 16384", derived.MaxWorkerSummaryChars)
	}
}

func TestSessionLimits32kDerived(t *testing.T) {
	derived := llm.ScaleLimitsFromTrueWindow(32768)
	got := settings.ApplyDerivedSessionLimits(settings.SessionLimits{}, derived)
	if got.MaxToolResultBytes != 65536 {
		t.Fatalf("MaxToolResultBytes = %d want 65536", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 32 {
		t.Fatalf("MaxCoordinatorLoopCycles = %d want 32", got.MaxCoordinatorLoopCycles)
	}
	if derived.MaxWorkerSummaryChars != 2048 {
		t.Fatalf("MaxWorkerSummaryChars = %d want 2048", derived.MaxWorkerSummaryChars)
	}
}

func TestSessionLimitsOverlayWins(t *testing.T) {
	derived := llm.ScaleLimitsFromTrueWindow(262144)
	overlay := settings.SessionLimits{MaxToolResultBytes: 99999}
	got := settings.ApplyDerivedSessionLimits(overlay, derived)
	if got.MaxToolResultBytes != 99999 {
		t.Fatalf("MaxToolResultBytes = %d want overlay 99999", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("MaxCoordinatorLoopCycles = %d want derived 256", got.MaxCoordinatorLoopCycles)
	}
	// Overlay still wins at a different true_window.
	got32 := settings.ApplyDerivedSessionLimits(overlay, llm.ScaleLimitsFromTrueWindow(32768))
	if got32.MaxToolResultBytes != 99999 {
		t.Fatalf("MaxToolResultBytes = %d want overlay 99999 at 32k", got32.MaxToolResultBytes)
	}
}

func TestSessionLimitsSkipsUnlistedFields(t *testing.T) {
	base := settings.DefaultSessionLimits()
	wantTimeout := base.LLMTurnTimeoutSec
	wantStall := base.LLMStreamStallTimeoutSec
	wantAwait := base.AwaitParentWorkersTimeoutSec
	got := settings.ApplyDerivedSessionLimits(base, llm.ScaleLimitsFromTrueWindow(32768))
	if got.LLMTurnTimeoutSec != wantTimeout {
		t.Fatalf("LLMTurnTimeoutSec changed: %d → %d", wantTimeout, got.LLMTurnTimeoutSec)
	}
	if got.LLMStreamStallTimeoutSec != wantStall {
		t.Fatalf("LLMStreamStallTimeoutSec changed: %d → %d", wantStall, got.LLMStreamStallTimeoutSec)
	}
	if got.AwaitParentWorkersTimeoutSec != wantAwait {
		t.Fatalf("AwaitParentWorkersTimeoutSec changed: %d → %d", wantAwait, got.AwaitParentWorkersTimeoutSec)
	}
}

func TestApplyLiveBudgetWorkerSummaryOverlayWins(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.MaxWorkerSummaryChars = 99999
	windows := modelinfo.DefaultModelContextWindows()
	out, _, err := llm.ApplyLiveBudget(cfg, llm.ModelPolicy{
		Coordinator: llm.ModelRef{Model: "accounts/fireworks/models/kimi-k2p7-code"},
	}, nil, windows)
	testutil.FailErr(t, "llm.ApplyLiveBudget failed", err)
	if out.MaxWorkerSummaryChars != 99999 {
		t.Fatalf("MaxWorkerSummaryChars = %d want overlay 99999", out.MaxWorkerSummaryChars)
	}
}
