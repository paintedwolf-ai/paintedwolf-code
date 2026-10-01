package settings

import (
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// EvaluateHostRule returns the winning host rule: a deny over any ask, then
// the most specific pattern, then the later rule.
func EvaluateHostRule(rules []ApprovalRule, host string) (ApprovalRule, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ApprovalRule{}, false
	}
	var matches []ApprovalRule
	for _, r := range rules {
		if r.Category == ApprovalCategoryHost && matchHostPattern(r.Pattern, host) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return ApprovalRule{}, false
	}
	best := matches[0]
	for _, r := range matches[1:] {
		if !rulePrecedenceLess(r, best) {
			best = r
		}
	}
	return best, true
}

// EvaluateHostRuleLayers preserves deny precedence across policy layers.
func EvaluateHostRuleLayers(layers ApprovalRuleLayers, host string) (ApprovalRule, bool) {
	device, deviceOK := EvaluateHostRule(layers.Device, host)
	project, projectOK := EvaluateHostRule(layers.Project, host)
	if deviceOK && device.Effect == ApprovalEffectDeny {
		return device, true
	}
	if projectOK && project.Effect == ApprovalEffectDeny {
		return project, true
	}
	if deviceOK {
		return device, true
	}
	return project, projectOK
}

// EvaluateWriteRootRule returns the winning exact normalized rule.
func EvaluateWriteRootRule(rules []ApprovalRule, root string) (ApprovalRule, bool) {
	root = confine.NormalizeWriteRootKey(root)
	if root == "" {
		return ApprovalRule{}, false
	}
	var matches []ApprovalRule
	for _, rule := range rules {
		if rule.Category != ApprovalCategoryWriteRoot {
			continue
		}
		if confine.NormalizeWriteRootKey(rule.Pattern) == root {
			matches = append(matches, rule)
		}
	}
	return winningRule(matches)
}

// EvaluateWriteRootRuleLayers preserves deny precedence across policy layers.
func EvaluateWriteRootRuleLayers(layers ApprovalRuleLayers, root string) (ApprovalRule, bool) {
	device, deviceOK := EvaluateWriteRootRule(layers.Device, root)
	project, projectOK := EvaluateWriteRootRule(layers.Project, root)
	if deviceOK && device.Effect == ApprovalEffectDeny {
		return device, true
	}
	if projectOK && project.Effect == ApprovalEffectDeny {
		return project, true
	}
	if deviceOK {
		return device, true
	}
	return project, projectOK
}

// matchHostPattern supports exact, wildcard, and domain-suffix patterns.
func matchHostPattern(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	switch pattern {
	case "":
		return false
	case "*":
		return true
	case host:
		return true
	}
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return host == suffix || strings.HasSuffix(host, "."+suffix)
	}
	if suffix, ok := strings.CutPrefix(pattern, "."); ok {
		return host == suffix || strings.HasSuffix(host, "."+suffix)
	}
	return matchGlob(pattern, host)
}
