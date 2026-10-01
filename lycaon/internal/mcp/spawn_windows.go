//go:build windows

package mcp

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hardenSpawn gives an MCP stdio subprocess its own process group so signals
// sent to the sidecar are not forwarded to it implicitly.
func hardenSpawn(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// reapProcessGroup terminates the subprocess after the SDK's graceful shutdown.
// Windows has no process-group SIGKILL equivalent here, so this kills the
// leader as a best-effort backstop once the session is closed.
func reapProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
