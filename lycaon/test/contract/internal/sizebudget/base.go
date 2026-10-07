package sizebudget

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// BaseEnv names the commit a change is measured against. scripts/budgets.py
// resolves it (scripts/change_scope.py); suites run without it skip base checks.
const BaseEnv = "PW_CHANGE_BASE"

// GrandfatherGrowth finds grandfathered caps added or raised since base.
func GrandfatherGrowth(base, head Policy) []Finding {
	var out []Finding
	for name, entries := range head.Grandfathered {
		for id, headCap := range entries {
			baseCap, ok := base.Grandfathered[name][id]
			if ok && headCap <= baseCap {
				continue
			}
			out = append(out, Finding{Category: name, ID: id, Kind: GrandfatherRaised, Measured: headCap, Bound: baseCap, Entry: EntryGrandfathered})
		}
	}
	sortFindings(out)
	return out
}

// BasePolicy reads the policy file as it stood at the change base. ok is
// false, with a note explaining why, when there is nothing to compare: no
// base was given, the file did not exist there, or it used an earlier shape.
func BasePolicy(root, path string, decode func([]byte) (Policy, error)) (policy Policy, ok bool, note string, err error) {
	base := os.Getenv(BaseEnv)
	if base == "" {
		return Policy{}, false, "no change base; grandfathered caps were not compared", nil
	}
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", base+"^{commit}").CombinedOutput(); err != nil {
		return Policy{}, false, "", fmt.Errorf("change base %s is not a commit: %s", base, strings.TrimSpace(string(out)))
	}
	if exec.Command("git", "-C", root, "cat-file", "-e", base+":"+path).Run() != nil {
		return Policy{}, false, fmt.Sprintf("%s does not exist at base %s", path, short(base)), nil
	}
	raw, err := exec.Command("git", "-C", root, "show", base+":"+path).Output()
	if err != nil {
		return Policy{}, false, "", fmt.Errorf("read %s at base %s: %w", path, short(base), err)
	}
	policy, decodeErr := decode(raw)
	if decodeErr != nil {
		reason, _, _ := strings.Cut(decodeErr.Error(), "\n")
		return Policy{}, false, fmt.Sprintf("%s at base %s predates this policy shape (%s); grandfathered caps were not compared", path, short(base), reason), nil
	}
	return policy, true, "", nil
}

func short(commit string) string { return commit[:min(len(commit), 12)] }
