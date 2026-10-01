package loopwake

import "strings"

// HostTurnWaitOnly reports a host turn that ended with only wait().
func HostTurnWaitOnly(turnTools []string) bool {
	if len(turnTools) != 1 {
		return false
	}
	return strings.TrimSpace(strings.ToLower(turnTools[0])) == "wait"
}
