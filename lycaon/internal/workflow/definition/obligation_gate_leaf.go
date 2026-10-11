package definition

import (
	"github.com/lycaon/lycaon/internal/conditions"
	"strings"
)

func ObligationGateLeaf(kind string) string {
	return conditions.ObligationGatePrefix + strings.TrimSpace(kind)
}
