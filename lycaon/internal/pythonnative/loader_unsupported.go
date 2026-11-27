//go:build !cgo || !((darwin && arm64) || (linux && amd64))

package pythonnative

import "runtime"

// NativeAOTSupported reports false when cgo is disabled or on unsupported OS/arch.
func NativeAOTSupported() bool { return false }

// Library is an unavailable handle placeholder on unsupported builds.
type Library struct{}

// Instance is an unavailable guest instance handle on unsupported builds.
type Instance struct{}

// Open fails explicitly with UnsupportedCapabilityError when cgo is disabled
// or the target tuple is not a supported shipping native AOT tuple.
func Open(_ string) (*Library, error) {
	return nil, &UnsupportedCapabilityError{
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
		Reason: "native AOT engine loader requires CGO_ENABLED=1 on darwin/arm64 or linux/amd64",
	}
}

func (*Library) Identity() *Identity                                   { return nil }
func (*Library) Init() Status                                          { return StatusInvalidArg }
func (*Library) NewInstance(InstanceConfig, WasiDispatchFunc) (*Instance, Status) {
	return nil, StatusInvalidArg
}
func (*Library) Close() {}

func (*Instance) Run() RunResult { return RunResult{Status: StatusInvalidArg} }
func (*Instance) Cancel()        {}
func (*Instance) Free()          {}
