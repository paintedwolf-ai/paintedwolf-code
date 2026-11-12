package gate

import (
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// A reviewed request answers its contact question, not independent policy
// reasons. Exercise their composition without using Evaluate as the oracle.
func TestRequestAuthorityPreservesIndependentReasons(t *testing.T) {
	for _, posture := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
		for mask := 0; mask < 4; mask++ {
			for _, opaque := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/reasons-%d/opaque-%t", posture, mask, opaque), func(t *testing.T) {
					facts := reviewedRequestFacts(opaque)
					var want []api.ApprovalGate
					if mask&1 != 0 {
						facts.UserRule = &UserRule{Category: "network", Pattern: "service.test"}
						want = append(want, api.GateUserRule)
					}
					if mask&2 != 0 {
						facts.Detection = &Match{PackID: "fixture", RuleID: "independent-policy", Level: "critical", External: true, Unrecoverable: true}
						want = append(want, api.GateAuthorityMisuse)
					}
					verdict, decision := Evaluate(facts, posture)
					if len(want) == 0 {
						if verdict != Silent || len(decision.Gates()) != 0 {
							t.Fatalf("covered request added review: %s, gates %v", verdict, decision.Gates())
						}
						return
					}
					if verdict != Ask || len(decision.Gates()) != len(want) {
						t.Fatalf("independent reasons lost or contact asked twice: %s, gates %v; want %v", verdict, decision.Gates(), want)
					}
					for _, reason := range want {
						if !slices.Contains(decision.Gates(), reason) {
							t.Fatalf("request authority suppressed independent reason %s: %v", reason, decision.Gates())
						}
					}
				})
			}
		}
	}
}

func TestRequestAuthorityCannotReplaceRequiredFacts(t *testing.T) {
	for _, missing := range []Producer{ProducerContainment, ProducerDestination, ProducerLease, ProducerRule, ProducerDetection, ProducerExposure} {
		t.Run(fmt.Sprintf("missing-%d", missing), func(t *testing.T) {
			facts := reviewedRequestFacts(true)
			facts.Ran &^= missing
			verdict, decision := Evaluate(facts, PostureStrict)
			if verdict != Ask || !slices.Contains(decision.Gates(), api.GateIncompleteFacts) {
				t.Fatalf("request authority replaced absent producer %d: %s, gates %v", missing, verdict, decision.Gates())
			}
			facts.Ran |= missing
			verdict, decision = Evaluate(facts, PostureStrict)
			if verdict != Silent {
				t.Fatalf("restoring facts did not recover covered request: %s, gates %v", verdict, decision.Gates())
			}
		})
	}
}

func reviewedRequestFacts(opaque bool) Facts {
	return Facts{
		Stage:         StagePreDial,
		Ran:           ProducerContainment | ProducerDestination | ProducerLease | ProducerRule | ProducerDetection | ProducerExposure,
		Containment:   Containment{FSJailed: true, Egress: EgressProxy},
		Destination:   &Endpoint{Host: "service.test", Port: 443, Opaque: opaque, FirstUseThisSession: true},
		SecretExposed: true, RequestConsented: true,
	}
}
