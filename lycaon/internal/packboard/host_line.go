package packboard

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// FormatHostLine renders sidecar host facts for pack board orientation.
func FormatHostLine(host *api.BoardHostSlice) string {
	if host == nil {
		return ""
	}
	osName := strings.TrimSpace(host.OS)
	arch := strings.TrimSpace(host.Arch)
	if osName == "" || arch == "" {
		return ""
	}
	line := fmt.Sprintf("Host: %s/%s · %s", osName, arch, hostExecutionLabel(host.ExecutionTarget))
	if host.Shell {
		line += " · shell"
	}
	return line
}

func hostExecutionLabel(target api.ExecutionTarget) string {
	switch target {
	case api.ExecutionTargetRunner:
		return "remote runner"
	default:
		return "local sidecar"
	}
}

// FormatToolchainsLine renders discovered toolchains for pack board orientation.
func FormatToolchainsLine(host *api.BoardHostSlice) string {
	if host == nil || len(host.Toolchains) == 0 {
		return ""
	}
	return "Toolchains: " + strings.Join(host.Toolchains, " · ")
}
