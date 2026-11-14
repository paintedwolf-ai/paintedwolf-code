package prompts

import (
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// commandFlags is what command may do beyond reading, as prompts describe it.
type commandFlags struct {
	mutation      bool
	externalExec  bool
	networkEgress bool
}

func allCommandEffects() toolcontract.CommandEffects {
	effects, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	return effects
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

// currentCommandEffectFlags reads the flags a variable map already states.
// A map that states none describes an offered command without restriction.
func currentCommandEffectFlags(vars map[string]any, commandOffered bool) commandFlags {
	read := func(key string) bool {
		if value, ok := vars[key].(bool); ok {
			return value
		}
		return commandOffered
	}
	return commandFlags{
		mutation:      read("profile_command_mutation"),
		externalExec:  read("profile_command_external_exec"),
		networkEgress: read("profile_command_network_egress"),
	}
}

// ScopeCommandEffects is what the shipped catalogs let command do under a
// coordinator surface, tool profile, or agent id. A scope they do not declare
// is bounded by its own profile where the prompt is rendered.
func ScopeCommandEffects(scopeID string) toolcontract.CommandEffects {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" || scopeID == CoordinatorProfileID {
		return allCommandEffects()
	}
	if row, err := loadCoordinatorSurfaceRow(scopeID); err == nil {
		effects, err := toolcontract.NewCommandEffects(row.CommandEffects...)
		if err != nil {
			return 0
		}
		return effects
	}
	if profileID, err := ToolProfileForAgent(scopeID); err == nil && profileID != "" {
		scopeID = profileID
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return allCommandEffects()
	}
	for _, profile := range profiles {
		if profile.ID == scopeID {
			return profile.CommandEffects
		}
	}
	return allCommandEffects()
}

// coordinatorSurfaceCommandEffects narrows a surface row by the coordinator
// profile the session resolved, which carries the session posture.
func coordinatorSurfaceCommandEffects(surfaceID string, profiles []sandbox.ToolProfile) toolcontract.CommandEffects {
	effects := ScopeCommandEffects(surfaceID)
	for _, profile := range profiles {
		if profile.ID == CoordinatorProfileID {
			return effects & profile.CommandEffects
		}
	}
	return effects
}

// RestrictCommandEffects narrows every variable derived from what command may
// do. Restrictions intersect: applied in any order, the variables state only
// what every one of them allows.
func RestrictCommandEffects(effects toolcontract.CommandEffects, offered, requestable []string, into map[string]any) {
	if into == nil {
		return
	}
	loadNativeToolFamilies()
	offeredSet := toolNameSet(offered)
	current := currentCommandEffectFlags(into, offeredSet["command"])
	allowed := commandEffectFlags(true, effects)
	command := commandFlags{
		mutation:      current.mutation && allowed.mutation,
		externalExec:  current.externalExec && allowed.externalExec,
		networkEgress: current.networkEgress && allowed.networkEgress,
	}
	into["profile_command_mutation"] = command.mutation
	into["profile_command_external_exec"] = command.externalExec
	into["profile_command_network_egress"] = command.networkEgress
	into["profile_has_terminal_capture"] = command.externalExec
	into["mints"] = evidenceMintsForCommand(offeredSet, command.mutation, command.externalExec)
	into["tool_check_verify_tools"] = verifyToolsForCheck(offeredSet, command.externalExec)
	if !command.mutation {
		enabled := toolNameSet(append(append([]string(nil), offered...), requestable...))
		delete(enabled, "command")
		into["profile_mutation_capable"] = toolSetMutationCapable(enabled)
		into["profile_has_code_rewrite"] = false
		into["profile_has_jq_edit"] = false
		writeTools := orderedEnabledTools(nativeWriteTools, offeredSet)
		into["profile_has_write_tools"] = len(writeTools) > 0
		into["tool_check_write_tools"] = writeTools
		into["progress_gated_tools"] = progressGatedOfferedTools(offeredSet, false)
	}
	MergeRunnerCapabilityVars(offered, into)
}
