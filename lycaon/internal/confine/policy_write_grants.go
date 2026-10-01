package confine

import (
	"fmt"
)

// ValidatePolicyWriteGrants validates agent-policy grants below roots or a
// registered project before review and launch. A grant names one file a loader
// reads, or a directory inside a tree a loader reads whole (a skill, prompt,
// rule, or workflow tree).
func ValidatePolicyWriteGrants(grants []ProtectedPathGrant, roots []string) ([]ProtectedPathGrant, error) {
	classify := AgentPolicyClassifier(roots...)
	for _, g := range grants {
		approved, approvedOK := classify(g.ApprovedPath)
		resolved, resolvedOK := classify(g.ResolvedPath)
		if !approvedOK || !resolvedOK || approved != resolved {
			return nil, fmt.Errorf("agent policy write approval must name a file an agent-policy loader reads, or a directory inside a skill, prompt, rule, or workflow tree")
		}
		if g.Subtree && resolved.Dir == "" {
			return nil, fmt.Errorf("agent policy write approval for %s must name the file, not a directory", g.ApprovedPath)
		}
	}
	applied, dropped, err := validateProtectedPathGrants(grants)
	if err != nil {
		return nil, err
	}
	if len(dropped) != 0 {
		return nil, fmt.Errorf("agent policy write approval cannot apply to %s: %s", dropped[0].Grant.ApprovedPath, dropped[0].Reason)
	}
	return applied, nil
}
