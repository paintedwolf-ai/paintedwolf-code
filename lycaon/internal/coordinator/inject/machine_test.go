package inject

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestStampMachineRequiresCompile(t *testing.T) {
	frame := CoordinatorTurnFrame{}
	StampMachine(&frame, Machine{})
	if frame.Machine.Compiled() {
		t.Fatal("zero machine must not stamp")
	}
	StampMachine(&frame, Machine{
		ProfileID:  "coordinator",
		SkillCount: 2,
		Surface:    prompts.AgentPromptSurface{Fingerprint: "fp"},
		ReadRoots:  []string{"/skills/a"},
	})
	if !frame.Machine.Compiled() || frame.Machine.SkillCount != 2 || frame.Machine.Surface.Fingerprint != "fp" {
		t.Fatalf("stamped machine = %+v", frame.Machine)
	}
	StampMachine(nil, Machine{ProfileID: "coordinator"})
}
