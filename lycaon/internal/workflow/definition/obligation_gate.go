package definition

import (
	"github.com/lycaon/lycaon/internal/conditions"
	"strings"
)

// ObligationKindFromGateLeaf extracts the kind from obligation_settled:<kind>.
func ObligationKindFromGateLeaf(leaf string) (string, bool) {
	leaf = strings.TrimSpace(leaf)
	if !strings.HasPrefix(leaf, conditions.ObligationGatePrefix) {
		return "", false
	}
	kind := strings.TrimSpace(strings.TrimPrefix(leaf, conditions.ObligationGatePrefix))
	return kind, kind != ""
}
