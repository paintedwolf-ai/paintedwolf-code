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
	if strings.ContainsAny(path, "\x00\r\n") {
		return ""
	}
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	info, err := os.Stat(path) //nolint:gosec // G703 — validated absolute socket path
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return path
}
