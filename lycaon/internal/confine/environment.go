package confine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// EnvironmentOverrides describes explicit startup changes to approval boundaries.
type EnvironmentOverrides struct {
	SandboxDisabled          bool     `json:"sandbox_disabled"`
	ApprovalsBypassed        bool     `json:"approvals_bypassed"`
	ControlPlaneReadsAllowed bool     `json:"control_plane_reads_allowed"`
	AdditionalWriteRoots     []string `json:"additional_write_roots,omitempty"`
}

// CurrentEnvironmentOverrides uses the same resolvers as profile construction.
func CurrentEnvironmentOverrides() EnvironmentOverrides {
	return EnvironmentOverrides{
		SandboxDisabled: SandboxDisabled(), ApprovalsBypassed: BypassEnabled(),
		ControlPlaneReadsAllowed: secretReadDenyDisabled(),
		AdditionalWriteRoots:     environmentWriteRoots(),
	}
}

func environmentWriteRoots() []string {
	var roots []string
	seen := make(map[string]bool)
	for _, root := range filepath.SplitList(os.Getenv("LYCAON_SANDBOX_WRITE_ROOTS")) {
		if strings.TrimSpace(root) == "" {
			continue
		}
		resolved := fspath.CanonicalPath(root)
		if resolved != "" && !seen[resolved] {
			seen[resolved] = true
			roots = append(roots, resolved)
		}
	}
	return roots
}
