//go:build darwin

package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

const agentSocketLookupTimeout = 2 * time.Second

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

func validAgentSocket(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return ""
	}
	info, err := os.Stat(path) //nolint:gosec // G703 — validated absolute socket path
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return filepath.Clean(path)
}
