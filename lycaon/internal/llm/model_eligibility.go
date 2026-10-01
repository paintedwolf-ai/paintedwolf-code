package llm

import (
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/pkg/api"
)

// ModelRoleEligibility is shared by assignment, readiness and presentation.
func ModelRoleEligibility(kind string, model modelinfo.Entry, role string, exclusions RoleExclusions) api.ModelRoleEligibility {
	result := api.ModelRoleEligibility{State: "eligible", Selectable: true, Code: "compatible", Reason: "The required capabilities are supported."}
	if role != PolicySlotCoordinator && role != PolicySlotAgentPool && role != PolicySlotLite {
		return api.ModelRoleEligibility{State: "incompatible", Code: "unknown_role", Reason: "This model role is not supported."}
	}
	if rule := exclusions.Match(kind, model.ID, role); rule != nil {
		return api.ModelRoleEligibility{State: "incompatible", Code: "role_excluded", Reason: rule.Reason, RuleID: rule.ID}
	}
	caps := model.EffectiveCapabilities()
	requirements := []struct {
		name     string
		evidence modelinfo.CapabilityEvidence
	}{{"chat", caps.Chat}}
	if role != PolicySlotLite {
		requirements = append(requirements, struct {
			name     string
			evidence modelinfo.CapabilityEvidence
		}{"tools", caps.Tools})
	}
	for _, requirement := range requirements {
		if modelinfo.Refused(requirement.evidence) {
			return api.ModelRoleEligibility{State: "incompatible", Code: requirement.name + "_unsupported", Reason: "The provider reports that this model does not support " + requirement.name + "."}
		}
		if !modelinfo.Supported(requirement.evidence) && result.State == "eligible" {
			result = api.ModelRoleEligibility{State: "unverified", Selectable: result.Selectable && requirement.name == "tools", Code: requirement.name + "_unknown", Reason: "Support for " + requirement.name + " is not listed for this model."}
		}
	}
	return result
}

func modelRolesToAPI(kind string, model modelinfo.Entry, doc RoleExclusions) api.ModelRoleEligibilitySet {
	return api.ModelRoleEligibilitySet{
		Coordinator: ModelRoleEligibility(kind, model, PolicySlotCoordinator, doc),
		AgentPool:   ModelRoleEligibility(kind, model, PolicySlotAgentPool, doc),
		Lite:        ModelRoleEligibility(kind, model, PolicySlotLite, doc),
	}
}

func ReadyToAssignExistsSlot(configured bool, kind string, models []modelinfo.Entry, doc RoleExclusions) bool {
	if !configured {
		return false
	}
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		for _, role := range []string{PolicySlotCoordinator, PolicySlotLite} {
			if ModelRoleEligibility(kind, model, role, doc).Selectable {
				return true
			}
		}
	}
	return false
}
