package batch

import (
	"strings"
)

// hostToWirePhase maps host Phase constants to OpenAPI CoordinatorBatchPhase wire values.
var hostToWirePhase = map[string]string{
	PhasePreDispatch: "", // host-only scaffold
	PhaseDispatch:    "dispatch",
	PhaseIntegrate:   "integrate",
	PhaseSynthesize:  "synthesize",
	PhaseClosed:      "closed",
}

// ToWirePhase maps a host scaffold phase to the Den / OpenAPI wire enum value.
// Returns empty when the phase should not surface on the wire (pre_dispatch or unknown).
func ToWirePhase(hostPhase string) string {
	p := strings.TrimSpace(strings.ToLower(hostPhase))
	if p == "" {
		return ""
	}
	if w, ok := hostToWirePhase[p]; ok {
		return w
	}
	return ""
}
