package gate

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecutionCapabilitiesRequireExplicitAuthority(t *testing.T) {
	for _, posture := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
		for _, mode := range []string{"process_control", "host_execution", "list", "signal"} {
			facts := baseFacts(StagePreSpawn)
			facts.Recoverable = true
			switch mode {
			case "process_control":
				facts.Containment.ProcessControl = true
			case "host_execution":
				facts.Containment.HostExecution = true
				facts.Containment.FSJailed = false
			default:
				facts.ProcessAccess = mode
				facts.Containment.SpawnsProcess = false
			}
			facts.Leased = true
			verdict, decision := Evaluate(facts, posture)
			if verdict != Ask || decision == nil || decision.Primary != api.GateUnobservedChannel {
				t.Fatalf("%s %s did not ask despite only predicate lease: %v %+v", posture, mode, verdict, decision)
			}
			reuse := decision.Reuse()
			if reuse.Shape != ReusePredicate || reuse.Scope != ScopeChat || reuse.DayScope() != ScopeChat {
				t.Fatalf("%s widened reuse: %+v", mode, reuse)
			}
			facts.LeasedExact = true
			_, decision = Evaluate(facts, posture)
			if decision != nil {
				for _, gate := range decision.Gates() {
					if gate == api.GateUnobservedChannel {
						t.Fatalf("%s ignored exact authority", mode)
					}
				}
			}
		}
	}
}
