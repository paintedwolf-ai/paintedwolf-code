package command

import (
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
)

// confineLimits serves the evaluator's resource-limit port from the confine
// owner's declared capabilities and the host process's own limits.
type confineLimits struct{}

func (confineLimits) Capability(resource string) commandprogram.LimitCapability {
	declared := confine.ResourceLimitCapability(resource)
	var capability commandprogram.LimitCapability
	for _, pair := range []struct {
		from confine.LimitCapability
		to   commandprogram.LimitCapability
	}{{confine.LimitQuery, commandprogram.LimitQuery}, {confine.LimitSetSoft, commandprogram.LimitSetSoft}, {confine.LimitSetHard, commandprogram.LimitSetHard}} {
		if declared&pair.from != 0 {
			capability |= pair.to
		}
	}
	return capability
}

func (confineLimits) Read(resource string) (commandprogram.ResourceLimit, error) {
	limit, err := confine.ReadResourceLimit(resource)
	return commandprogram.ResourceLimit{Resource: limit.Resource, Soft: limit.Soft, Hard: limit.Hard}, err
}

// childLimits converts the limits ulimit pinned into the launch attributes.
func childLimits(limits []commandprogram.ResourceLimit) []confine.ResourceLimit {
	if len(limits) == 0 {
		return nil
	}
	out := make([]confine.ResourceLimit, len(limits))
	for i, limit := range limits {
		out[i] = confine.ResourceLimit{Resource: limit.Resource, Soft: limit.Soft, Hard: limit.Hard}
	}
	return out
}
