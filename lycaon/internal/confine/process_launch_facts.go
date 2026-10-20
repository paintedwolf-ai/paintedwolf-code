package confine

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// LaunchMechanism names the selected native primitive BEFORE launch admission.
// The held binary primitive changes AT_EXECFN, never the supplied argv vector.
type LaunchMechanism string

const (
	StoppedPathLaunch LaunchMechanism = "stopped_path_exec"
	HeldBinaryLaunch  LaunchMechanism = "held_binary_procfd_exec"
	UnsupportedLaunch LaunchMechanism = "unavailable"
)

// ProcessLaunchFacts distinguishes mechanism guarantees from later observations.
// These operation-local facts are not a new durable or wire receipt shape.
type ProcessLaunchFacts struct {
	Mechanism                                                            LaunchMechanism
	SelectedEntryBound, DirectoryBound, ArgumentsBound, EnvironmentBound bool
	AuxiliaryExecutableName                                              string
	PID                                                                  int
	StartTime                                                            int64
	Namespaces                                                           [3]string
	Security                                                             osprocess.ProcessSecurity
	SecurityObserved                                                     bool
	SecurityError                                                        error
}

func (p *ProcessPins) LaunchMechanism() LaunchMechanism {
	p.mu.Lock()
	defer p.mu.Unlock()
	return platformLaunchMechanism(p)
}

// RequireStoppedObservation checks a caller's stronger explicit requirement.
// It does not silently select a different launch primitive or grant authority.
func (p *ProcessPins) RequireStoppedObservation() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || platformLaunchMechanism(p) != StoppedPathLaunch {
		return fmt.Errorf("%w: selected mechanism has no postexec/preinstruction observation barrier", osprocess.ErrExecutableObservationUnavailable)
	}
	return nil
}

// LaunchFacts returns a detached snapshot. Missing observations remain unknown;
// a successful kernel handoff does not establish complete output or effects.
func (p *ProcessPins) LaunchFacts() (ProcessLaunchFacts, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.launchFacts
	if out.Security.Linux != nil {
		copy := *out.Security.Linux
		out.Security.Linux = &copy
	}
	return out, p.launchFactsPresent
}
