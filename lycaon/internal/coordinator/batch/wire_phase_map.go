package batch

import (
	"sort"
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

// HostPhases returns every host phase key in the map, sorted.
func HostPhases() []string {
	out := make([]string, 0, len(hostToWirePhase))
	for p := range hostToWirePhase {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// WirePhases returns the mapped host phases that appear on the wire, sorted.
func WirePhases() []string {
	set := map[string]struct{}{}
	for _, w := range hostToWirePhase {
		if w == "" {
			continue
		}
		set[w] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}
