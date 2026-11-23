package platform

import (
	"runtime"

	"github.com/lycaon/lycaon/pkg/api"
)

// BoardHost returns stable sidecar host facts for pack board orientation.
func BoardHost(execTarget api.ExecutionTarget) *api.BoardHostSlice {
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
