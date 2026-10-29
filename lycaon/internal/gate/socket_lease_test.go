package gate

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestActionLeaseCannotAuthorizeUncoveredSocketObject(t *testing.T) {
	for _, posture := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
		facts := baseFacts(StagePreSpawn)
		facts.Containment = Containment{FSJailed: true, Egress: EgressProxy, SocketCount: 1, SocketPathsDigest: "fresh-object"}
		facts.Leased = true
		verdict, decision := Evaluate(facts, posture)
		if verdict != Ask || decision.Primary != api.GateUnobservedChannel {
			t.Fatalf("uncovered socket at %s: %s, %+v", posture, verdict, decision)
		}
		found := false
		for _, cited := range decision.Cited {
			if cited.Key == "boundary.sockets" && cited.Value == "1" && cited.Source == "confine" {
				found = true
			}
		}
		if !found {
			t.Fatal("fresh socket review lost structured boundary provenance")
		}
		facts.Containment.SocketCount = 0
		facts.Containment.SocketPathsDigest = ""
		if verdict, decision := Evaluate(facts, posture); verdict != Silent {
			t.Fatalf("explicitly covered sockets at %s: %s, %+v", posture, verdict, decision)
		}
	}
}
