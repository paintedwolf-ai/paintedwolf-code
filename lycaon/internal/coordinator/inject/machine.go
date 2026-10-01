package inject

import (
	"strings"

	"github.com/lycaon/lycaon/internal/prompts"
)

// Machine is the session/project compile for one turn: host-resource
// snapshot, gated skills, and the prompt surface derived from them. Computed
// once per turn; every consumer reads it.
type Machine struct {
	ProfileID  string
	SkillCount int
	Surface    prompts.AgentPromptSurface
	ReadRoots  []string
}

// Compiled reports whether this value was produced by a turn compile.
func (m Machine) Compiled() bool {
	return strings.TrimSpace(m.ProfileID) != ""
}

// StampMachine copies a compiled machine onto a frame. No-op when either
// side is missing or the machine was never compiled.
func StampMachine(frame *CoordinatorTurnFrame, machine Machine) {
	if frame == nil || !machine.Compiled() {
		return
	}
	frame.Machine = machine
}
