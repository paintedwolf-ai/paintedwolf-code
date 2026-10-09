package definition

import (
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
)

func ObligationGateLeaf(kind string) string {
	return conditions.ObligationGatePrefix + strings.TrimSpace(kind)
}
