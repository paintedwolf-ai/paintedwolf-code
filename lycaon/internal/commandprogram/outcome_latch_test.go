package commandprogram

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func completedOutcome(status int) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect, PublicationEligible: status == 0}
}

func TestOrdinaryFailureAndNativeComparisonRemainConditionalStatuses(t *testing.T) {
	for _, comparison := range []bool{false, true} {
		latch := &OutcomeLatch{}
		result := completedOutcome(1)
		if comparison {
			result.PublicationEligible = true
			result.PublicationBasis = toolcontract.NativeComparisonPublication
		}
		testutil.FailErr(t, "record ordinary nonzero", latch.Record("fixture-stage", result, true))
		testutil.FailErr(t, "admit conditional continuation", latch.AdmitNext())
		if result.Status != 1 {
			t.Fatal("comparison status was normalized to success")
		}
		if _, stopped := latch.Stop(); stopped {
			t.Fatal("ordinary nonzero became host stop")
		}
	}
}

func TestLaterSuccessCannotEraseAuthoritativeStop(t *testing.T) {
	for _, tc := range []struct {
		name          string
		result        toolcontract.ProducerResult
		inputComplete bool
		kind          HostStopKind
	}{
		{"refused", toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "SURVEY_PATH_ESCAPE", EffectCertainty: toolcontract.NoEffect}, true, StopAuthority},
		{"canceled", toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, true, StopInterrupted},
		{"budget", toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, StopCode: "COMMAND_LIMIT_EXCEEDED", EffectCertainty: toolcontract.EffectCompleted}, true, StopOwner},
		{"required_input", completedOutcome(0), false, StopRequiredInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			latch := &OutcomeLatch{}
			testutil.FailErr(t, "retain earlier completed effect", latch.Record("earlier-write", toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}, true))
			testutil.FailErr(t, "record stopping fact", latch.Record("stopping-stage", tc.result, tc.inputComplete))
			testutil.FailErr(t, "observe later zero status", latch.Record("explicit-exit-zero", completedOutcome(0), true))
			var stopped *AdmissionStoppedError
			if !errors.As(latch.AdmitNext(), &stopped) || stopped.Stop.Kind != tc.kind || stopped.Stop.ExecutionID != "stopping-stage" || stopped.Stop.Result.EffectCertainty != tc.result.EffectCertainty {
				t.Fatalf("later success erased stop: %+v", stopped)
			}
		})
	}
}

func TestInvalidProducerCannotGrantFurtherAdmission(t *testing.T) {
	latch := &OutcomeLatch{}
	invalid := completedOutcome(1)
	invalid.PublicationEligible = true
	if err := latch.Record("invalid-stage", invalid, true); err == nil {
		t.Fatal("unclassified external nonzero admitted publication")
	}
	stop, ok := latch.Stop()
	if !ok || stop.Kind != StopInvalidResult || latch.AdmitNext() == nil {
		t.Fatal("invalid producer did not fail closed")
	}
}
