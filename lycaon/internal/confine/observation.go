package confine

import "strings"

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
	// Refusals is what the kernel reported refusing the invocation.
	Refusals SandboxRefusals
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
