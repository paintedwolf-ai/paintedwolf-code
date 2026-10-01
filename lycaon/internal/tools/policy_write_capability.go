package tools

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

// preparePolicyWrite adds the exact agent-policy path to the normal approval ask.
func (e *DefaultToolExecutor) preparePolicyWrite(tc *ToolContext, path string) error {
	if e.policy == nil || e.approvalGate == nil || e.checkpointMgr == nil {
		return fmt.Errorf("file change approval is not configured")
	}
	grant := confine.NewProtectedPathGrant(path)
	if _, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{grant}, ConfineRootsForAction(*tc)); err != nil {
		return err
	}
	tc.PolicyWriteGrants = append(tc.PolicyWriteGrants, grant)
	return nil
}

// policyWriteTargets classifies the approved policy grants for the gate.
func policyWriteTargets(req confine.Request, projectDir string) []hitl.AgentPolicyTarget {
	var out []hitl.AgentPolicyTarget
	for _, path := range policyWritePaths(req) {
		if target, ok := hitl.AgentPolicyTargetFor(path, projectDir, req.Roots...); ok {
			out = append(out, target)
		}
	}
	return out
}

func policyWritePaths(req confine.Request) []string {
	paths := make([]string, 0, len(req.PolicyWriteGrants))
	for _, grant := range req.PolicyWriteGrants {
		paths = append(paths, grant.ResolvedPath)
	}
	return paths
}
