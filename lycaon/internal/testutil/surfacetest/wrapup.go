package surfacetest

import "github.com/lycaon/lycaon/internal/coordinator/surface"

// WithWrapupGates records computed synthesis gates.
func WithWrapupGates(state surface.ImplementSessionState, batchReady, openRepair bool) surface.ImplementSessionState {
	state.WrapupGatesLoaded = true
	state.BatchReadyForSynthesis = batchReady
	state.OpenRepairSinceUserIntent = openRepair
	return state
}
