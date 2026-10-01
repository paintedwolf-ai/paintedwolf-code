//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// hardenSpawn places an MCP stdio subprocess in its own process group and, where
// the platform supports it, arms a parent-death signal. The group lets Close
// reap the whole tree; the death signal kills the server if the sidecar exits
// abnormally (SIGKILL, crash) before it can shut sessions down.
func hardenSpawn(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	setParentDeathSignal(cmd.SysProcAttr)
}

// reapProcessGroup SIGKILLs the subprocess's process group after the SDK has
// shut the leader down. The leader PID doubles as the group ID (Setpgid), and
// the kernel reserves it as long as any member survives, so this reaches
// grandchildren the server spawned even once the leader itself has exited.
func reapProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
