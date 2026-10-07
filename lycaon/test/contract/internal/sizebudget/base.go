package sizebudget

import (
	"fmt"
	"os/exec"
	"strings"
)

// BasePolicy reads the policy file as it stood at the change base. ok is
// false, with a note saying why, when there is nothing to compare: the file
// did not exist there, or it used an earlier shape.
func BasePolicy(root, base, path string, decode func([]byte) (Policy, error)) (policy Policy, ok bool, note string, err error) {
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
		return Policy{}, false, fmt.Sprintf("%s at base %s predates this policy shape (%s); exceptions were not compared", path, short(base), reason), nil
	}
	return policy, true, "", nil
}

func short(commit string) string { return commit[:min(len(commit), 12)] }
