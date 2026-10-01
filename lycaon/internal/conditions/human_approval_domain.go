package conditions

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ShippedHumanApprovalDomainIDs returns generic human_approval gate vocabulary.
func ShippedHumanApprovalDomainIDs() []string {
	return []string{"human_approval"}
}

// RegisterHumanApprovalDomain registers the generic human_approval gate evaluator.
func RegisterHumanApprovalDomain(reg *ConditionRegistry, deps RegistryDeps) error {
	if reg == nil {
		return nil
	}
	if reg.Has("human_approval") {
		return nil
	}
	return reg.Register("human_approval", func(ec EvalContext) (bool, error) {
		return humanApprovalSatisfied(ec, deps)
	})
}

func humanApprovalSatisfied(ec EvalContext, deps RegistryDeps) (bool, error) {
	if !DotPathTruthy(ec.Vars, "human_approval.issued") {
		return false, nil
	}
	if !DotPathTruthy(ec.Vars, "human_approval.ready") {
		return false, nil
	}
	blueprintPath, _ := dotPathString(ec.Vars, "human_approval.blueprint_path")
	if strings.TrimSpace(blueprintPath) == "" {
		return false, nil
	}
	stored, _ := dotPathString(ec.Vars, "human_approval.blueprint_hash")
	if strings.TrimSpace(stored) == "" {
		return false, nil
	}
	if deps.BlueprintContent == nil {
		return false, nil
	}
	content, err := deps.BlueprintContent(ec.Ctx, ec.ProjectDir, blueprintPath)
	if err != nil || strings.TrimSpace(content) == "" {
		return false, err
	}
	return humanApprovalContentMatches(stored, content), nil
}

func humanApprovalContentMatches(storedHash, content string) bool {
	storedHash = strings.TrimSpace(storedHash)
	if storedHash == "" || strings.TrimSpace(content) == "" {
		return false
	}
	sum := sha256.Sum256([]byte(content))
	return storedHash == hex.EncodeToString(sum[:])
}

func dotPathString(vars map[string]any, path string) (string, bool) {
	if vars == nil {
		return "", false
	}
	cur := any(vars)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[part]
		if !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}
