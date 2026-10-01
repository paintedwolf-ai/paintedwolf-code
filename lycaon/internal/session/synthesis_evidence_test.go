package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
)

func TestShouldInjectSynthesisEvidenceBlock(t *testing.T) {
	ready := surface.ImplementSessionState{WrapupGatesLoaded: true, BatchReadyForSynthesis: true}
	closed := surface.ImplementSessionState{WrapupGatesLoaded: true, BatchReadyForSynthesis: false}
	inFlight := surface.ImplementSessionState{WorkersInFlight: 1, WrapupGatesLoaded: true, BatchReadyForSynthesis: true}

	cases := []struct {
		name      string
		state     surface.ImplementSessionState
		surfaceID string
		want      bool
	}{
		{name: "implement wrapup when gates hold", state: ready, surfaceID: "implement_synthesis", want: true},
		{name: "investigate wrapup when gates hold", state: ready, surfaceID: "implement_investigate", want: true},
		{name: "empty surface follows wrapup gates", state: ready, surfaceID: "", want: true},
		{name: "implement wrapup closed when batch not ready", state: closed, surfaceID: "implement_synthesis", want: false},
		{name: "review adjudicate without wrapup gates", state: closed, surfaceID: "review_adjudicate", want: true},
		{name: "decision adjudicate without wrapup gates", state: closed, surfaceID: "decision_adjudicate", want: true},
		{name: "review adjudicate while workers fly", state: inFlight, surfaceID: "review_adjudicate", want: false},
		{name: "other surface stays closed", state: ready, surfaceID: "implement_routing", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInjectSynthesisEvidenceBlock(tc.state, tc.surfaceID); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
