package confine

import (
	"slices"
	"strings"
)

const (
	SignalSocksProxy                     = "socks_proxy"
	SignalBoundaryRefused                = "boundary_refused"
	SignalRemotePackageDestinationDenied = "remote_package_destination_denied"
	DenialSubjectPackageHost             = "remote_package_destination"
)

// Observation holds boundary facts from one invocation.
type Observation struct {
	Applied       bool
	Network       string
	DenialSubject string
	Destination   string
	// Port is the broker-observed destination port.
	Port    int
	Signals []string
	// FailedStages are the command lines of the finished invocation's failed
	// stages; empty while it runs.
	FailedStages []string
	// Running reports an invocation observed before it ended.
	Running bool
	// TerminationReason is the executor-observed terminal state, if known.
	TerminationReason string
	// Refusals is what the kernel reported refusing the invocation.
	Refusals SandboxRefusals
}

// Merge combines boundary observations across stages of one invocation.
func (o Observation) Merge(other Observation) Observation {
	if !other.Applied {
		return o
	}
	if !o.Applied {
		return other
	}
	merged := o
	merged.Signals = append([]string(nil), o.Signals...)
	merged.FailedStages = append([]string(nil), o.FailedStages...)
	if merged.Network == "" {
		merged.Network = other.Network
	}
	if merged.DenialSubject == "" {
		merged.DenialSubject = other.DenialSubject
	}
	if merged.Destination == "" && other.Destination != "" {
		merged.Destination = other.Destination
		merged.Port = other.Port
	}
	for _, signal := range other.Signals {
		merged.add(signal)
	}
	for _, stage := range other.FailedStages {
		if stage != "" && !slices.Contains(merged.FailedStages, stage) {
			merged.FailedStages = append(merged.FailedStages, stage)
		}
	}
	merged.Running = o.Running || other.Running
	if merged.TerminationReason == "" || (other.TerminationReason != "" && other.TerminationReason != "exited") {
		merged.TerminationReason = other.TerminationReason
	}
	merged.Refusals = o.Refusals.Merge(other.Refusals)
	return merged
}

// HasSignal reports whether signal was observed on this invocation.
func (o Observation) HasSignal(signal string) bool {
	signal = strings.TrimSpace(signal)
	if signal == "" {
		return false
	}
	for _, existing := range o.Signals {
		if existing == signal {
			return true
		}
	}
	return false
}

func (o *Observation) add(signal string) {
	signal = strings.TrimSpace(signal)
	if signal == "" || o.HasSignal(signal) {
		return
	}
	o.Signals = append(o.Signals, signal)
}
