package platform

import (
	"runtime"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

var (
	boardHostMu       sync.RWMutex
	boardHostOverride *api.BoardHostSlice
)

// BoardHost returns stable sidecar host facts for pack board orientation.
func BoardHost(execTarget api.ExecutionTarget) *api.BoardHostSlice {
	boardHostMu.RLock()
	override := boardHostOverride
	boardHostMu.RUnlock()
	if override != nil {
		copy := *override
		return &copy
	}
	if execTarget == "" {
		execTarget = api.ExecutionTargetLocal
	}
	return &api.BoardHostSlice{
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		ExecutionTarget: execTarget,
		Shell:           true,
	}
}
