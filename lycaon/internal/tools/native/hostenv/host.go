package hostenv

import (
	"context"
	"errors"
	"runtime"
)

// SystemFacts are the platform's uname fields.
type SystemFacts struct {
	Sysname, Nodename, Release, Version, Machine string
}

// IdentityFacts are the effective identity of the host process that runs
// program children. Names are empty where the platform does not resolve them.
type IdentityFacts struct {
	User       string
	UID, GID   int
	Groups     []int
	GroupNames map[int]string
}

// HostFacts answers identity and system queries. OSHost reads the actual
// platform; callers that need deterministic answers inject their own facts.
type HostFacts interface {
	System(context.Context) (SystemFacts, error)
	Identity(context.Context) (IdentityFacts, error)
	// Processors is the number of processing units available to the host
	// process (nproc), and with all every unit installed (nproc --all).
	Processors(ctx context.Context, all bool) (int, error)
}

// ErrHostFactUnavailable marks a fact this platform cannot report.
var ErrHostFactUnavailable = errors.New("host fact unavailable on this platform")

// OSHost reports the running platform's facts.
type OSHost struct{}

var _ HostFacts = OSHost{}

// Processors reports the units Go schedules on: the CPU affinity set where
// the platform has one, which is also the installed count elsewhere.
func (OSHost) Processors(context.Context, bool) (int, error) { return runtime.NumCPU(), nil }
