package prompts

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// commandFlags is what command may do beyond reading, as prompts describe it.
type commandFlags struct {
	mutation      bool
	externalExec  bool
	networkEgress bool
}

// commandEffectFlags projects an effect set. An empty set allows nothing.
func commandEffectFlags(commandEnabled bool, effects toolcontract.CommandEffects) commandFlags {
	allows := func(class toolcontract.CommandEffectClass) bool {
		required, err := toolcontract.NewCommandEffects(class)
		return commandEnabled && err == nil && effects.Allows(required)
	}
	return commandFlags{
		mutation:      allows(toolcontract.CommandMutation),
		externalExec:  allows(toolcontract.CommandExternalExec),
		networkEgress: allows(toolcontract.CommandNetworkEgress),
	}
}
