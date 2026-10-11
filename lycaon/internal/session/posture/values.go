package posture

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// AllSessionPostures is the closed posture enum.
func AllSessionPostures() []api.SessionPosture {
	return []api.SessionPosture{
		api.SessionPostureSpec,
		api.SessionPostureBuild,
		api.SessionPostureOrchestrate,
		api.SessionPostureVet,
	}
}

// ValidSessionPosture reports whether s is a known SessionPosture value.
func ValidSessionPosture(s string) bool {
	switch api.SessionPosture(strings.TrimSpace(s)) {
	case api.SessionPostureSpec,
		api.SessionPostureBuild,
		api.SessionPostureOrchestrate,
		api.SessionPostureVet:
		return true
	default:
		return false
	}
}
