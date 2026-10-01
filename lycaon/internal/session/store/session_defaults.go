package store

import (
	"strings"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

// ChildSessionPosture keeps implementation workers in build posture.
func ChildSessionPosture(parent api.SessionPosture, agentType string) api.SessionPosture {
	if strings.TrimSpace(agentType) == orchestration.ProfileImplementer {
		return api.SessionPostureBuild
	}
	if parent != "" {
		return parent
	}
	return api.SessionPostureBuild
}

// defaultRootSessionAgentType returns agent_type for new sessions.
// Root coordinator shells default to coordinator; worker children set agent_type at spawn.
func defaultRootSessionAgentType(parentID string) string {
	if strings.TrimSpace(parentID) != "" {
		return ""
	}
	return orchestration.ProfileCoordinator
}

func normalizeSessionModelRef(providerID, model string) (string, string, error) {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if (providerID == "") != (model == "") {
		return "", "", ErrSessionModelRefIncomplete
	}
	return providerID, model, nil
}
