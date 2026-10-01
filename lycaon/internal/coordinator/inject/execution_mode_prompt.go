package inject

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"strconv"
	"strings"
)

// TransitionInjectKey fingerprints ephemeral transition inject for one user/host prompt turn.
func TransitionInjectKey(sessionID string, promptTurnSeq int, transition surface.TransitionVars) string {
	return hashString(strings.Join([]string{
		strings.TrimSpace(sessionID),
		strconv.Itoa(promptTurnSeq),
		strings.TrimSpace(transition.ExecutionModeEntered),
		strings.TrimSpace(transition.ExecutionModeLeft),
	}, "\x00"))
}

// ShouldRenderTransitionInject reports whether entered/left partials would render this turn.
func ShouldRenderTransitionInject(transition surface.TransitionVars) bool {
	return strings.TrimSpace(transition.ExecutionModeEntered) != "" ||
		strings.TrimSpace(transition.ExecutionModeLeft) != ""
}
