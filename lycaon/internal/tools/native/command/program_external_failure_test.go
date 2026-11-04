package command

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestExternalLaunchFailurePreservesTypedRefusal(t *testing.T) {
	fallback := toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
	rejection := errors.Join(errors.New("launch admission"), &toolrejection.ToolReject{Code: "ROOT_NOT_ATTACHED"})
	got := externalLaunchFailure(rejection, fallback)
	if got.Status != 2 || got.Terminal != toolcontract.ProducerRefused || got.StopCode != "ROOT_NOT_ATTACHED" || got.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("typed launch refusal = %+v", got)
	}
	if ordinary := externalLaunchFailure(errors.New("executable unavailable"), fallback); ordinary != fallback {
		t.Fatalf("ordinary launch failure = %+v, want %+v", ordinary, fallback)
	}
}

func TestObservationMergePreservesEarlierRefusalsAndFailedStagesAcrossPipeline(t *testing.T) {
	first := confine.Observation{
		Applied:           true,
		Signals:           []string{confine.SignalBoundaryRefused},
		FailedStages:      []string{"/usr/local/bin/gh pr view"},
		TerminationReason: "exited",
		Refusals: confine.SandboxRefusals{
			Witness: confine.WitnessKernel,
			Refusals: []confine.SandboxRefusal{
				{
					Operation: "file-read-data",
					Target:    "/Users/person/Library/Keychains/login.keychain-db",
					Count:     1,
					Recovery:  confine.RecoverReadPath,
					Grant:     "/Users/person/Library/Keychains/login.keychain-db",
				},
			},
		},
	}
	second := confine.Observation{
		Applied:           true,
		TerminationReason: "exited",
		Refusals: confine.SandboxRefusals{
			Witness: confine.WitnessKernel,
		},
	}

	merged := first.Merge(second)
	if !merged.Applied || !merged.HasSignal(confine.SignalBoundaryRefused) {
		t.Fatalf("merged signals lost refusal: %+v", merged)
	}
	if !slices.Equal(merged.FailedStages, []string{"/usr/local/bin/gh pr view"}) {
		t.Fatalf("merged failed stages = %v", merged.FailedStages)
	}
	if len(merged.Refusals.Refusals) != 1 || merged.Refusals.Refusals[0].Grant != "/Users/person/Library/Keychains/login.keychain-db" {
		t.Fatalf("merged refusals = %+v", merged.Refusals)
	}

	repeated := merged.Merge(confine.Observation{
		Applied:      true,
		FailedStages: []string{"/usr/local/bin/gh pr view", "/usr/bin/curl https://api.github.com"},
		Refusals: confine.SandboxRefusals{
			Witness: confine.WitnessIncomplete,
			Refusals: []confine.SandboxRefusal{
				{
					Operation: "file-read-data",
					Target:    "/Users/person/Library/Keychains/login.keychain-db",
					Count:     2,
					Recovery:  confine.RecoverReadPath,
					Grant:     "/Users/person/Library/Keychains/login.keychain-db",
				},
			},
		},
	})
	if repeated.Refusals.Witness != confine.WitnessIncomplete {
		t.Fatalf("merged witness = %q, want %q", repeated.Refusals.Witness, confine.WitnessIncomplete)
	}
	if len(repeated.Refusals.Refusals) != 1 || repeated.Refusals.Refusals[0].Count != 3 {
		t.Fatalf("merged count = %+v", repeated.Refusals.Refusals)
	}
	if !slices.Equal(repeated.FailedStages, []string{"/usr/local/bin/gh pr view", "/usr/bin/curl https://api.github.com"}) {
		t.Fatalf("merged failed stages = %v", repeated.FailedStages)
	}
}
