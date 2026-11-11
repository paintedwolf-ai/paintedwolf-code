package gate

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// The outbound gates as executable policy: readable and opaque destinations
// meet the same authority, a declared retrieval capability stays within its
// own contract, Light stays quieter, and Strict adds first contact and
// exposure.
func TestOutboundPolicyMatrix(t *testing.T) {
	type dest struct {
		name                                   string
		opaque, configured, retrieval, firstUse bool
	}
	dests := []dest{
		{name: "readable new", firstUse: true},
		{name: "opaque new", opaque: true, firstUse: true},
		{name: "readable contacted"},
		{name: "readable configured new", configured: true, firstUse: true},
		{name: "retrieval new", retrieval: true, firstUse: true},
	}
	want := map[Posture]map[string]api.ApprovalGate{
		PostureLight: {},
		PostureBalanced: {
			"readable new": api.GateAgentChosenOutbound,
			"opaque new":   api.GateAgentChosenOutbound,
		},
		PostureStrict: {
			"readable new":            api.GateAgentChosenOutbound,
			"opaque new":              api.GateAgentChosenOutbound,
			"readable configured new": api.GateFirstHost,
		},
	}
	for _, posture := range Postures() {
		for _, d := range dests {
			f := baseFacts(StagePreDial)
			f.Destination = &Endpoint{
				Host: "service.example", Transport: "http", Opaque: d.opaque,
				Configured: d.configured, Retrieval: d.retrieval, FirstUseThisSession: d.firstUse,
			}
			verdict, decision := Evaluate(f, posture)
			expected, asks := want[posture][d.name]
			switch {
			case asks && (verdict != Ask || decision.Primary != expected):
				t.Errorf("%s %s: verdict %v %+v, want %s", posture, d.name, verdict, decision, expected)
			case !asks && verdict == Ask:
				t.Errorf("%s %s: asked %s, want silent", posture, d.name, decision.Primary)
			}
		}
	}

	for _, opaque := range []bool{true, false} {
		f := baseFacts(StagePreDial)
		f.SecretExposed = true
		f.Destination = &Endpoint{Host: "api.example", Transport: "http", Configured: true, Opaque: opaque}
		if verdict, decision := Evaluate(f, PostureStrict); verdict != Ask || decision.Primary != api.GateSecretExposedOutbound {
			t.Errorf("strict exposure with opaque=%v: %v %+v", opaque, verdict, decision)
		}
		f.Destination.Retrieval = true
		if verdict, decision := Evaluate(f, PostureStrict); verdict == Ask {
			t.Errorf("strict exposure on a retrieval dial with opaque=%v asked %+v", opaque, decision)
		}
	}
}
