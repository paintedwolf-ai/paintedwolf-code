package compaction

import (
	"testing"
)

// Uncalibrated sessions keep the additive cold-start reserve, and the floor at
// TargetTokens still holds when the reserve would swallow the whole ceiling.
func TestFitMaxTokensColdStartReserveAndFloor(t *testing.T) {
	cfg := CompactionConfig{
		HardCeilingTokens: 111411,
		TargetTokens:      40000,
	}
	got := FitMaxTokens(cfg, PromptTokenCalibration{}, 20000)
	// Quantized down to a 2048 multiple so the fitted prefix start stays stable.
	if want := (111411 - 20000) / fitOverheadQuantum * fitOverheadQuantum; got != want {
		t.Fatalf("FitMaxTokens = %d want %d", got, want)
	}
	if got > cfg.HardCeilingTokens-20000 {
		t.Fatalf("FitMaxTokens = %d must not exceed ceiling−reserve %d", got, cfg.HardCeilingTokens-20000)
	}
	if got < cfg.TargetTokens {
		t.Fatalf("FitMaxTokens = %d want >= TargetTokens %d", got, cfg.TargetTokens)
	}

	floored := FitMaxTokens(cfg, PromptTokenCalibration{}, 200000)
	if floored != cfg.TargetTokens {
		t.Fatalf("oversized reserve FitMaxTokens = %d want TargetTokens %d", floored, cfg.TargetTokens)
	}
}

// Once calibrated, the budget is expressed in transcript-estimate space: the fit
// measures with EstimateMessagesTokens, so its ceiling has to be in the same units.
// A session billing 1.5× its estimate gets ~2/3 of the ceiling as estimate budget.
func TestFitMaxTokensUsesCalibratedEstimateSpace(t *testing.T) {
	cfg := CompactionConfig{HardCeilingTokens: 167116, TargetTokens: 40000}
	cal := PromptTokenCalibration{ReportedPromptTokens: 150000, TranscriptEstimate: 100000}

	got := FitMaxTokens(cfg, cal, 15000)
	want := 167116 * 100000 / 150000 / fitOverheadQuantum * fitOverheadQuantum
	if got != want {
		t.Fatalf("FitMaxTokens = %d want %d", got, want)
	}
	// A transcript at this estimate projects to no more than the ceiling once
	// the provider bills it.
	if projected := cal.ProjectBilled(got, 15000); projected > cfg.HardCeilingTokens {
		t.Fatalf("budget %d projects to %d billed, over ceiling %d",
			got, projected, cfg.HardCeilingTokens)
	}
	// The fitted budget stays below the billed-token ceiling.
	if got >= cfg.HardCeilingTokens {
		t.Fatalf("calibrated budget %d must sit below the ceiling %d", got, cfg.HardCeilingTokens)
	}
}

// The budget must not move when the reported prompt_tokens jitters: an unquantized
// budget shifts the fitted prefix start on nearly every call and invalidates the
// provider prompt cache mid-history.
func TestFitMaxTokensStableAcrossReportedJitter(t *testing.T) {
	cfg := CompactionConfig{HardCeilingTokens: 167116, TargetTokens: 40000}
	base := FitMaxTokens(cfg, PromptTokenCalibration{
		ReportedPromptTokens: 150000, TranscriptEstimate: 100000,
	}, 15000)
	for _, reported := range []int{150001, 150200, 149900} {
		got := FitMaxTokens(cfg, PromptTokenCalibration{
			ReportedPromptTokens: reported, TranscriptEstimate: 100000,
		}, 15000)
		if got != base {
			t.Fatalf("reported %d gave budget %d, want %d (same quantum bucket)",
				reported, got, base)
		}
	}
	if base%fitOverheadQuantum != 0 {
		t.Fatalf("budget %d is not quantized to %d", base, fitOverheadQuantum)
	}
}

func TestResolveColdStartOverhead(t *testing.T) {
	if got := ResolveColdStartOverhead(""); got != defaultColdStartFitOverheadTokens {
		t.Fatalf("empty tools = %d want %d", got, defaultColdStartFitOverheadTokens)
	}
	hugeTools := string(make([]byte, defaultColdStartFitOverheadTokens*4+400))
	if got := ResolveColdStartOverhead(hugeTools); got <= defaultColdStartFitOverheadTokens {
		t.Fatalf("toolsJSON cold-start = %d want > %d", got, defaultColdStartFitOverheadTokens)
	}
}
