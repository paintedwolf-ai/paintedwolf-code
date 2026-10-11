package runstate

import (
	"strings"
)

const implementWorkLegPrefix = "implement-work:"

// ImplementWorkLegKey is the locked anchor.LegFinished leg id for implement@ work-phase kicks.
func ImplementWorkLegKey(sessionID string) string {
	return implementWorkLegPrefix + strings.TrimSpace(sessionID)
}
