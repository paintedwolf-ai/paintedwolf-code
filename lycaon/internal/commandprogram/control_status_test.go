package commandprogram

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestConditionalSequenceNegationAndExitTruthTables(t *testing.T) {
	for _, status := range []int{0, 1, 2, 255} {
		for _, negated := range []bool{false, true} {
			latch := &OutcomeLatch{}
			flow, err := NewStatusControl(latch)
			testutil.FailErr(t, "create status control", err)
			result := completedOutcome(status)
			testutil.FailErr(t, "record ordinary status", flow.Record("fixture-status", result, true, negated))
			want := status
			if negated {
				if status == 0 {
					want = 1
				} else {
					want = 0
				}
			}
			for _, connector := range []StatusConnector{StatusSequence, StatusAnd, StatusOr} {
				got, err := flow.ShouldRun(connector)
				testutil.FailErr(t, "select reached continuation", err)
				expected := connector == StatusSequence || (connector == StatusAnd && want == 0) || (connector == StatusOr && want != 0)
				if got != expected {
					t.Fatalf("status=%d negated=%t connector=%v got=%t", status, negated, connector, got)
				}
			}
			testutil.FailErr(t, "explicit exit", flow.Exit(7))
			if run, err := flow.ShouldRun(StatusSequence); err != nil || run || !flow.Exited() || flow.Status() != 7 {
				t.Fatal("exit did not terminate remaining statements")
			}
			testutil.FailErr(t, "retain late completed child separately", flow.Record("late-child", completedOutcome(0), true, false))
			if flow.Status() != 7 {
				t.Fatal("late settlement erased explicit exit")
			}
		}
	}
}

func TestHostStopsCannotBecomeConditionsOrExplicitZeroExit(t *testing.T) {
	for _, terminal := range []toolcontract.ProducerTerminal{toolcontract.ProducerRefused, toolcontract.ProducerInterrupted} {
		flow, err := NewStatusControl(&OutcomeLatch{})
		testutil.FailErr(t, "create control", err)
		result := toolcontract.ProducerResult{Status: 1, Terminal: terminal, EffectCertainty: toolcontract.EffectUnknown}
		testutil.FailErr(t, "record host stop under negation", flow.Record("denied-child", result, true, true))
		if flow.Status() != 1 {
			t.Fatal("negation erased stopping status")
		}
		for _, connector := range []StatusConnector{StatusSequence, StatusAnd, StatusOr} {
			if run, err := flow.ShouldRun(connector); run || err == nil {
				t.Fatal("host stop admitted condition/sequence")
			}
		}
		if err := flow.Exit(0); err == nil || flow.Exited() {
			t.Fatal("exit0 erased host stop")
		}
	}
}

func TestFailedSubstitutionPreventsLaunchButOrdinarySequenceCanContinue(t *testing.T) {
	flow, err := NewStatusControl(&OutcomeLatch{})
	testutil.FailErr(t, "create control", err)
	launched := false
	if err := flow.AdmitSubstitution("substitution-child", PipelineOutcome{Status: 1, Complete: true}); err == nil {
		launched = true
	} else {
		var failure *SubstitutionFailure
		if !errors.As(err, &failure) || failure.Status != 1 {
			t.Fatalf("wrong substitution fact: %v", err)
		}
	}
	if launched {
		t.Fatal("failed substitution launched containing operation")
	}
	if run, err := flow.ShouldRun(StatusSequence); err != nil || !run {
		t.Fatal("ordinary nonzero substitution became host stop")
	}
	if err := flow.AdmitSubstitution("incomplete-substitution", PipelineOutcome{Status: 0, Complete: false}); err == nil {
		t.Fatal("incomplete required output admitted launch")
	}
	if run, err := flow.ShouldRun(StatusSequence); run || err == nil {
		t.Fatal("required input incompleteness allowed another effect")
	}
}

func TestExitStatusMisuseDoesNotWrapOrExit(t *testing.T) {
	flow, err := NewStatusControl(&OutcomeLatch{})
	testutil.FailErr(t, "create control", err)
	for _, status := range []int{-1, 256, 1000} {
		var misuse *StatusOperandError
		if !errors.As(flow.Exit(status), &misuse) || misuse.Value != status || flow.Exited() {
			t.Fatal("invalid exit status wrapped or exited")
		}
	}
}

func TestLatchedStopStatusSurvivesLateSuccessfulChild(t *testing.T) {
	flow, err := NewStatusControl(&OutcomeLatch{})
	testutil.FailErr(t, "create control", err)
	testutil.FailErr(t, "record refusal", flow.Record("refused", toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, EffectCertainty: toolcontract.EffectUnknown}, true, false))
	testutil.FailErr(t, "retain late child settlement", flow.Record("late-success", completedOutcome(0), true, false))
	if flow.Status() != 1 {
		t.Fatal("late successful child erased stopping status")
	}
}
func TestSubstitutionIdentityAndCompletenessCannotBeHiddenByStatus(t *testing.T) {
	for _, status := range []int{0, 1, 2} {
		flow, err := NewStatusControl(&OutcomeLatch{})
		testutil.FailErr(t, "create control", err)
		if err := flow.AdmitSubstitution("incomplete-child", PipelineOutcome{Status: status, Complete: false}); err == nil {
			t.Fatalf("incomplete status%d admitted", status)
		}
		if run, err := flow.ShouldRun(StatusSequence); run || err == nil {
			t.Fatal("incomplete required output allowed continuation")
		}
		flow, err = NewStatusControl(&OutcomeLatch{})
		testutil.FailErr(t, "create identity control", err)
		if err := flow.AdmitSubstitution("", PipelineOutcome{Status: status, Complete: true}); err == nil {
			t.Fatal("missing dynamic identity admitted")
		}
		if run, err := flow.ShouldRun(StatusSequence); run || err == nil {
			t.Fatal("missing identity left admission open")
		}
	}
}

func TestInvalidChildCannotLeaveVisibleSuccess(t *testing.T) {
	flow, err := NewStatusControl(&OutcomeLatch{})
	testutil.FailErr(t, "create control", err)
	if err := flow.Record("invalid-child", toolcontract.ProducerResult{Status: -1, Terminal: toolcontract.ProducerCompleted, EffectCertainty: toolcontract.EffectUnknown}, true, false); err == nil || flow.Status() != 2 {
		t.Fatal("invalid child retained visible success")
	}
}
