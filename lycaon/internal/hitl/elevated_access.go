package hitl

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ElevatedEffectsForAction captures boundary facts before exact action identities
// are reduced to digests. It describes authority, never whether it was used.
func ElevatedEffectsForAction(action ProposedAction) []api.ElevatedAccessEffect {
	var effects []api.ElevatedAccessEffect
	if action.Contained.DirectIP || action.Contained.Egress == ContainedEgressDirectIP {
		effects = append(effects, api.ElevatedAccessEffectDirectNetwork)
	}
	if action.Contained.HostExecution {
		effects = append(effects, api.ElevatedAccessEffectHostExecution)
	}
	if action.Contained.ProcessControl || action.ProcessAccess == "signal" {
		effects = append(effects, api.ElevatedAccessEffectProcessControl)
	}
	if action.Contained.SocketCount > 0 || len(action.SocketGrants) > 0 {
		effects = append(effects, api.ElevatedAccessEffectLocalService)
	}
	return effects
}

// ElevatedGrantEffects is shared by the composer and Saved approvals inventory.
func ElevatedGrantEffects(grant ApprovalGrant) []api.ElevatedAccessEffect {
	effects := slices.Clone(grant.ElevatedEffects)
	switch grant.Predicate.Category {
	case ApprovalGrantCategoryDirectIP:
		effects = append(effects, api.ElevatedAccessEffectDirectNetwork)
	case ApprovalGrantCategorySocketPath, ApprovalGrantCategorySocketCapability:
		effects = append(effects, api.ElevatedAccessEffectLocalService)
	case ApprovalGrantCategoryExecutionCapability:
		switch grant.Predicate.Pattern {
		case "host_execution":
			effects = append(effects, api.ElevatedAccessEffectHostExecution)
		case "process_control", "process_signal":
			effects = append(effects, api.ElevatedAccessEffectProcessControl)
		}
	}
	slices.Sort(effects)
	return slices.Compact(effects)
}

// elevatedQuietEffects consumes the gate's declared reason-segment grammar,
// before quiet keys acquire their action digest. Labels are never authority.
func elevatedQuietEffects(segment string) []api.ElevatedAccessEffect {
	name, subject, ok := strings.Cut(segment, ":")
	if !ok || name != string(api.GateUnobservedChannel) {
		return nil
	}
	var effect api.ElevatedAccessEffect
	switch {
	case subject == "direct_ip":
		effect = api.ElevatedAccessEffectDirectNetwork
	case subject == "host_execution" || subject == "unconfined":
		effect = api.ElevatedAccessEffectHostExecution
	case subject == "process_control" || subject == "native_process:signal":
		effect = api.ElevatedAccessEffectProcessControl
	case strings.HasPrefix(subject, "socket:"):
		effect = api.ElevatedAccessEffectLocalService
	default:
		return nil
	}
	return []api.ElevatedAccessEffect{effect}
}

// ValidateElevatedEffects rejects unknown persisted boundary descriptors.
func ValidateElevatedEffects(effects []api.ElevatedAccessEffect) error {
	for _, effect := range effects {
		switch effect {
		case api.ElevatedAccessEffectDirectNetwork, api.ElevatedAccessEffectHostExecution, api.ElevatedAccessEffectProcessControl, api.ElevatedAccessEffectLocalService:
		default:
			return fmt.Errorf("unknown elevated effect %q", effect)
		}
	}
	return nil
}
