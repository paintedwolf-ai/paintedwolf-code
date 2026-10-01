package mcp

import (
	"fmt"
	"os"
)

// DistroCommandSelf resolves to the current lycaon binary path in distro-mcp.yaml.
const DistroCommandSelf = "@self"

// ResolveDistroCommand expands @self to os.Executable() for pinned first-party MCP providers.
func ResolveDistroCommand(command string, args []string) (string, []string, error) {
	if command != DistroCommandSelf {
		return command, args, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolve @self: %w", err)
	}
	return exe, args, nil
}
