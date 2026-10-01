//go:build unix && !linux

package mcp

import "syscall"

// setParentDeathSignal is a no-op outside Linux: darwin and the BSDs expose no
// parent-death primitive, so the engine's process reaper kills the group instead.
func setParentDeathSignal(*syscall.SysProcAttr) {}
