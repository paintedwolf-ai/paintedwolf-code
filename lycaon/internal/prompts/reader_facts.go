package prompts

import (
	"slices"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func buildReaderFacts(
	in ReaderInput,
	profile sandbox.ToolProfile,
	profileID, surfaceID, mode string,
	offered, requestable []string,
	commandOnFloor bool,
	effects toolcontract.CommandEffects,
) (map[string]any, error) {
	out := make(map[string]any, 64)

	if surfaceID != "" || profileID == CoordinatorProfileID {
		var (
			scopeData CoordinatorPathScopeData
			err       error
		)
		if surfaceID == "implement_investigate" {
			scopeData, err = LoadInvestigatePathScopes()
		} else {
			scopeData, err = CoordinatorPathScopesFromProfiles(in.Profiles)
		}
		if err != nil {
			return nil, err
		}
		for k, v := range CoordinatorPathScopeTemplateVars(scopeData) {
			out[k] = v
		}
		for k, v := range CoordinatorPolicyTemplateVars(offered, requestable) {
			out[k] = v
		}
		spawn.RefreshSizingHintVars(out)
		out["is_coordinator"] = true
		out["on_worker_branch"] = in.OnWorkerBranch
		out["has_git"] = in.HasGit
		out["root_count"] = in.RootCount
		out["command_on_floor"] = commandOnFloor
		if surfaceID != "" {
			out["surface_id"] = surfaceID
		}
		if mode != "" {
			out["execution_mode"] = mode
		}
		restrictCommandEffects(effects, offered, requestable, out)
		syncCommandExternalFacts(out)
		canRequest := len(requestable) > 0 && slices.Contains(offered, "request_tools")
		if canRequest {
			roster, err := RequestableTools(requestable, in.Schemas)
			if err != nil {
				return nil, err
			}
			out["requestable_tools"] = requestableToolVars(roster)
			out["requestable_capabilities"] = RequestableCapabilities(roster)
		} else {
			out["requestable_tools"] = []map[string]any(nil)
			out["requestable_capabilities"] = []string(nil)
		}
		out["surface_offered"] = slices.Clone(offered)
		commandMutates := currentCommandEffectFlags(out, slices.Contains(offered, "command")).mutation
		if surfaceID != "" {
			card, err := LoadCoordinatorSurfaceCard(surfaceID)
			if err != nil {
				return nil, err
			}
			for k, v := range CoordinatorSurfaceCardVars(card, offered, requestable, commandMutates).TemplateVars() {
				out[k] = v
			}
		}
		runOffered := offered
		if !commandMutates {
			runOffered = filterToolName(offered, "command")
		}
		MergeVisualShowVars(runOffered, requestable, out)
		out["more_tools_loadable"] = canRequest
		out["web_search_enabled"] = in.WebSearch
		if !in.WebSearch {
			MergeWebResearchUnavailable(out)
		}
		clearSkillRosterWithoutSkillsRead(out)
		return out, nil
	}

	toolViews := make([]AgentToolArgView, 0, len(offered))
	for _, name := range offered {
		view := AgentToolArgView{Name: name}
		if in.Schemas != nil {
			if meta, ok := in.Schemas.ToolMeta(name); ok {
				view.RequiredArgs, _ = toolschema.ArgFieldSummary(meta.ArgsSchema)
			}
		}
		toolViews = append(toolViews, view)
	}
	roster, err := RequestableTools(requestable, in.Schemas)
	if err != nil {
		return nil, err
	}
	data := AgentToolSurfaceData{
		ToolProfile:     profileID,
		Tools:           toolViews,
		Requestable:     roster,
		WriteGlobs:      slices.Clone(profile.WriteGlobs),
		CommandEffects:  effects,
		CommandDeferred: !commandOnFloor && slices.Contains(offered, "command"),
	}
	for k, v := range AgentToolSurfaceTemplateVars(data) {
		out[k] = v
	}
	restrictCommandEffects(effects, offered, requestable, out)
	syncCommandExternalFacts(out)
	mergeVisualShowVarsFromSurface(data, out)
	out["is_coordinator"] = false
	out["on_worker_branch"] = in.OnWorkerBranch
	out["has_git"] = in.HasGit
	out["root_count"] = in.RootCount
	out["web_search_enabled"] = in.WebSearch
	return out, nil
}
