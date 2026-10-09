package session

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func sessionProjectKey(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.ProjectID)
}
