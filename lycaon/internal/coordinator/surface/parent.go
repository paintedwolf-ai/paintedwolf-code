package surface

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func IsCoordinatorParent(sess *api.Session) bool {
	if sess == nil || strings.TrimSpace(sess.ParentSessionID) != "" {
		return false
	}
	agent := strings.TrimSpace(sess.AgentType)
	if agent == "" {
		agent = "coordinator"
	}
	return agent == "coordinator"
}
