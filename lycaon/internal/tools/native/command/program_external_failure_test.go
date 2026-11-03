package command

import (
	"errors"
	"testing"

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
