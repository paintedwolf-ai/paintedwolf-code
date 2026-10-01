package compaction

import (
	"testing"
)

// Before the first provider observation there is nothing to scale from, so the
// additive cold-start reserve stands in.
func TestProjectBilledColdStart(t *testing.T) {
	var uncalibrated PromptTokenCalibration
	if got := uncalibrated.ProjectBilled(5000, 0); got != 5000 {
		t.Fatalf("ProjectBilled = %d want 5000", got)
	}
	if got := uncalibrated.ProjectBilled(90000, 22000); got != 112000 {
		t.Fatalf("ProjectBilled = %d want 112000", got)
	}
	if got := uncalibrated.ProjectBilled(5000, -100); got != 5000 {
		t.Fatalf("negative reserve should be ignored, got %d", got)
	}
}

// The gap between estimate and billed is proportional, not constant: a session
// billing 1.5× its estimate at 100k projects 150k at 100k and 225k at 150k.
func TestProjectBilledScalesWithTranscript(t *testing.T) {
	cal := PromptTokenCalibration{ReportedPromptTokens: 150000, TranscriptEstimate: 100000}
	if got := cal.ProjectBilled(100000, 15000); got != 150000 {
		t.Fatalf("at the observation point = %d want 150000", got)
	}
	if got := cal.ProjectBilled(150000, 15000); got != 225000 {
		t.Fatalf("grown transcript = %d want 225000 (scaled, not +50000)", got)
	}
}

// The projection scales by the ratio rather than reusing the last reported value,
// so after compaction shrinks the transcript the trigger clears.
func TestProjectBilledFallsAfterCompaction(t *testing.T) {
	cal := PromptTokenCalibration{ReportedPromptTokens: 205000, TranscriptEstimate: 145000}
	before := cal.ProjectBilled(145000, 15000)
	after := cal.ProjectBilled(20000, 15000)
	if after >= before {
		t.Fatalf("projection after compaction = %d, must fall below %d", after, before)
	}
	if after > 40000 {
		t.Fatalf("projection after compaction = %d, still implausibly high", after)
	}
}

// An estimate at or above the reported count means the estimator is not
// undercounting; the ratio floors at 1 rather than inflating the ceiling.
func TestProjectBilledRatioFloorsAtOne(t *testing.T) {
	cal := PromptTokenCalibration{ReportedPromptTokens: 90000, TranscriptEstimate: 100000}
	if got := cal.ProjectBilled(100000, 0); got != 100000 {
		t.Fatalf("got %d want the estimate itself (ratio floored at 1)", got)
	}
	budget := cal.EstimateBudget(167116, 0)
	if budget > 167116 {
		t.Fatalf("estimate budget %d must not exceed the ceiling", budget)
	}
}

// The absolute reserve is sized for a frontier window. On an 8192-token local
// window an unclamped 15000-token reserve would hold an empty session over the
// compaction trigger.
func TestColdStartOverheadClampedToWindow(t *testing.T) {
	small := CompactionConfig{ModelContextWindow: 8192}
	got := small.ColdStartOverhead(defaultColdStartFitOverheadTokens)
	if got >= small.ModelContextWindow {
		t.Fatalf("reserve %d must stay below the window %d", got, small.ModelContextWindow)
	}
	if want := 8192 * coldStartMaxWindowPct / 100; got != want {
		t.Fatalf("reserve = %d want %d", got, want)
	}

	// A frontier window is far above the cap, so the absolute reserve stands.
	big := CompactionConfig{ModelContextWindow: 196608}
	if got := big.ColdStartOverhead(defaultColdStartFitOverheadTokens); got != defaultColdStartFitOverheadTokens {
		t.Fatalf("large window reserve = %d want %d", got, defaultColdStartFitOverheadTokens)
	}

	// Unknown window: nothing to clamp against, and under-reserving is the failure
	// that costs money, so the reserve passes through.
	unknown := CompactionConfig{}
	if got := unknown.ColdStartOverhead(12345); got != 12345 {
		t.Fatalf("unknown window reserve = %d want 12345", got)
	}
	if got := big.ColdStartOverhead(0); got != 0 {
		t.Fatalf("zero reserve should stay zero, got %d", got)
	}
}

func TestPromptTokenCalibrationCalibrated(t *testing.T) {
	cases := map[string]struct {
		cal  PromptTokenCalibration
		want bool
	}{
		"zero":          {PromptTokenCalibration{}, false},
		"no estimate":   {PromptTokenCalibration{ReportedPromptTokens: 100}, false},
		"no reported":   {PromptTokenCalibration{TranscriptEstimate: 100}, false},
		"both positive": {PromptTokenCalibration{ReportedPromptTokens: 150, TranscriptEstimate: 100}, true},
	}
	for name, tc := range cases {
		if got := tc.cal.Calibrated(); got != tc.want {
			t.Fatalf("%s: Calibrated() = %v want %v", name, got, tc.want)
		}
	}
}
