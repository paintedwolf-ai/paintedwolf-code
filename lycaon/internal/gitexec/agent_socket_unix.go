//go:build darwin || linux

package gitexec

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const agentSocketLookupTimeout = 2 * time.Second

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
