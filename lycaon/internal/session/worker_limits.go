package session

import (
	"strings"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApplyWorkerMaxToolLoops overlays worker tool-loop caps onto session limits.
// A child-session override takes precedence over the configured default.
func ApplyWorkerMaxToolLoops(lim settings.SessionLimits, sess *api.Session) settings.SessionLimits {
	if sess == nil || strings.TrimSpace(sess.ParentSessionID) == "" {
		return lim
	}
	if sess.MaxToolLoops > 0 {
		lim.MaxIterations = sess.MaxToolLoops
		return lim
	}
	lim.MaxIterations = lim.WorkerToolBudgetDefault
	return lim
}
