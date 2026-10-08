package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/oar"
)

func TestTerminalRefusalsRetainPathsWithoutRecoverableGrants(t *testing.T) {
	gc := &oar.GuardContext{}
	ObserveConfine(gc, confine.Observation{Applied: true, FailedStages: []string{"tool"}, Refusals: confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{
		{Operation: "file-read-data", Target: "/state/store.db", Layer: confine.FloorReadControlPlane, Recovery: confine.RecoverNone},
		{Operation: "file-write-data", Target: "/state/approvals.yaml", Layer: confine.FloorControlPlane, Recovery: confine.RecoverNone},
	}}})
	if len(gc.RefusedTerminalReadPaths) != 1 || len(gc.RefusedTerminalWritePaths) != 1 || len(gc.RefusedReadGrants) != 0 || len(gc.RefusedWriteGrants) != 0 || gc.SandboxRefusalWitness != string(confine.WitnessKernel) {
		t.Fatalf("terminal facts=%+v", gc)
	}
}

func TestRefusalWitnessReportsTheObservedLayerWithoutPolicyCombination(t *testing.T) {
	for _, witness := range []confine.RefusalWitness{confine.WitnessKernel, confine.WitnessIncomplete, confine.WitnessUnavailable} {
		for _, failed := range []bool{false, true} {
			gc := &oar.GuardContext{}
			obs := confine.Observation{Applied: true, Refusals: confine.SandboxRefusals{Witness: witness}}
			if failed {
				obs.FailedStages = []string{"tool"}
			}
			ObserveConfine(gc, obs)
			if gc.SandboxRefusalWitness != string(witness) {
				t.Fatalf("witness=%s failed=%v facts=%+v", witness, failed, gc)
			}
		}
	}
}
