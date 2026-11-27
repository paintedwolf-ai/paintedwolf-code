// Package pythonnative is the declared platform adapter for Portable Python's
// native AOT engine loader (dlopen/dlsym cgo bridge) and host platform facts.
// Shared WASI dispatch, lifecycle, receipts, authority, and manifest logic live
// above this package in pythonruntime and portablepython.
package pythonnative

import (
	"fmt"
	"runtime"
)

// Status codes matching pw_py_status_t in pw_py_engine.h.
type Status uint32

const (
	StatusOK             Status = 0
	StatusProcExit       Status = 1
	StatusFuelExhausted  Status = 2
	StatusCanceled       Status = 3
	StatusMemoryLimit    Status = 4
	StatusHostStop       Status = 5
	StatusTrap           Status = 6
	StatusInvalidArg     Status = 7
	StatusThreadAffinity Status = 8
	StatusAllocFailed    Status = 9
)

// WasiDispatchFunc handles one guest WASI preview1 call with the live linear
// memory slice, canonical op ID (0..42), and u64-widened arguments.
type WasiDispatchFunc func(mem []byte, opID uint32, args []uint64) (action uint32, ret uint32)

// Identity mirrors pw_py_engine_identity_t reported by pw_py_engine_identity().
type Identity struct {
	ABIVersion            uint32
	ImportCount           uint32
	EngineTag             string
	SourceCommit          string
	Platform              string
	CPythonVersion        string
	CPythonWasmSHA256     string
	MeteredWasmSHA256     string
	WABTVersion           string
	FuelAlgorithmVersion  string
	ImportInventorySHA256 string
}

// InstanceConfig mirrors pw_py_instance_config_t.
type InstanceConfig struct {
	MaxMemoryPages uint32
	FuelBudget     uint64
}

// RunResult mirrors pw_py_run_result_t.
type RunResult struct {
	Status          Status
	ExitCode        uint32
	TrapCode        uint32
	PeakMemoryPages uint32
	FuelRemaining   int64
	GuestWorkUsed   uint64
}

// UnsupportedCapabilityError is returned when the running build lacks the
// native AOT engine loader capability (e.g. CGO_ENABLED=0 or an unsupported
// OS/architecture). Callers must fail closed and never silently fall back.
type UnsupportedCapabilityError struct {
	GOOS   string
	GOARCH string
	Reason string
}

func (e *UnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("portable python native AOT capability unsupported on %s/%s: %s", e.GOOS, e.GOARCH, e.Reason)
}

// HostPlatform returns the running host's (GOOS, GOARCH) tuple.
func HostPlatform() (string, string) {
	return runtime.GOOS, runtime.GOARCH
}
