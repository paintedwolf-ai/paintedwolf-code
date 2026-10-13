//go:build darwin

package gitexec

import (
	"context"
	"os"
	"strings"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

func defaultAgentSocket(ctx context.Context) string {
	if socket := validAgentSocket(os.Getenv("SSH_AUTH_SOCK")); socket != "" {
		return socket
	}
	ctx, cancel := context.WithTimeout(ctx, agentSocketLookupTimeout)
	defer cancel()
	out, code, err := execpkg.Run(ctx, "/bin/launchctl", []string{"getenv", "SSH_AUTH_SOCK"}, execpkg.ExecOpts{
		Launch:         execpkg.HostLaunch("ssh_agent_socket_lookup"),
		Env:            []string{},
		Timeout:        agentSocketLookupTimeout,
		MaxOutputBytes: 4096,
	})
	if err != nil || code != 0 {
		return ""
	}
	return validAgentSocket(strings.TrimSpace(string(out)))
}
