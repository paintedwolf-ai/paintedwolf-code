package tools

import "github.com/lycaon/lycaon/internal/confine"

// AgentLaunchOutcome is how a native owner's confined launch ended, for the
// command's confinement facts.
type AgentLaunchOutcome struct {
	// Failed reports a launch whose program ended unsuccessfully.
	Failed bool
	// Refusals are refusals the host's own services made inside the launch,
	// such as a credential request no grant covered.
	Refusals []confine.SandboxRefusal
}
