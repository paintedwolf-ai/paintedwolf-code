//go:build windows

package mcp

import "os/exec"

// hardenSpawn is a no-op on Windows because StartInOwnGroup assigns the MCP
// stdio subprocess to a job object in its own process group.
func hardenSpawn(*exec.Cmd) {}
