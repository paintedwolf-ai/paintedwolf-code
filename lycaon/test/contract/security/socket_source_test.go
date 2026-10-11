package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var socketCapabilityPaths = []string{
	"lycaon/internal/toolexecution/socket_capability.go",
	"lycaon/internal/toolexecution/invocation_boundary.go",
	"lycaon/internal/toolexecution/package_execution_boundary.go",
	"lycaon/internal/toolexecution/socket_realization.go",
	"lycaon/internal/toolexecution/socket_approval.go",
	"lycaon/internal/tools/socket_spawn.go",
	"lycaon/internal/capabilitygrants/socket_execution_grants.go",
	"lycaon/internal/tools/capability_lifecycle.go",
}

func socketCapabilitySources(t *testing.T, root string) string {
	t.Helper()
	var sources []string
	for _, path := range socketCapabilityPaths {
		sources = append(sources, contractcheck.ReadRepoFile(t, root, path))
	}
	return strings.Join(sources, "\n")
}
