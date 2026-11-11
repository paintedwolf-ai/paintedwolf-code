//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// hardenSpawn arms the platform parent-death signal before StartInOwnGroup
// places the MCP stdio subprocess in its own guarded session.
func hardenSpawn(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	setParentDeathSignal(cmd.SysProcAttr)
}
