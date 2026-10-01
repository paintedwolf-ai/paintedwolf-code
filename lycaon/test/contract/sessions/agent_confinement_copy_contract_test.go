package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Catalog confinement copy must not name cards, asks, grants, or leases.
func TestAgentFacingConfinementCopyOmitsApprovalSystem(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	surfaces := []string{
		"lycaon/config/packs/painted-wolf/platform/shared/partials/sandbox-confinement.md",
		"lycaon/config/packs/painted-wolf/platform/shared/units/coordinator-host-runner.md",
		"lycaon/config/packs/painted-wolf/platform/skills/reach-a-network-service/SKILL.md",
		"lycaon/config/packs/painted-wolf/platform/tools/schemas/command.yaml",
		"lycaon/config/packs/painted-wolf/platform/tools/schemas/terminal_open.yaml",
		"lycaon/config/packs/painted-wolf/platform/tools/schemas/verify.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_BOUNDARY_REFUSED.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_APPROVAL_UNAVAILABLE.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_CONTROL_PLANE_DENIED.yaml",
		"lycaon/config/packs/painted-wolf/platform/policy/VERIFY_UNVERIFIABLE.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_WRITE_ROOT_DENIED.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_TRY_WRITE_ROOT.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_READ_PATH_DENIED.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_TRY_READ_PATH.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_LOCAL_LISTEN_DENIED.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_LOCAL_NETWORK_DENIED.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_LOOPBACK_CONNECT_DENIED.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_LOCAL_LISTEN_REQUEST_INVALID.yaml",
		"lycaon/config/packs/painted-wolf/security/policy/SANDBOX_LOOPBACK_CONNECT_REQUEST_INVALID.yaml",
	}
	banned := []string{
		"approval card",
		"the host raises",
		"the host asks",
		"missing grant",
		"task lease",
		"Requests human approval",
		"raise a card",
		"write-root card",
		"without a new card",
		"Settings → Approvals",
		"request_decision",
		"not available this turn",
	}
	for _, rel := range surfaces {
		src := contractcheck.ReadRepoFile(t, root, rel)
		lower := strings.ToLower(src)
		for _, phrase := range banned {
			if strings.Contains(lower, strings.ToLower(phrase)) {
				t.Errorf("%s teaches the approval system (%q)", rel, phrase)
			}
		}
	}
}
