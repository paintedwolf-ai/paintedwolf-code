//go:build linux

package mcp

import "syscall"

// setParentDeathSignal asks the kernel to SIGKILL the child when the sidecar
// (its parent) dies. Linux-only; the engine's process reaper also covers the
// rest of the group and the other unixes.
func setParentDeathSignal(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGKILL
}
