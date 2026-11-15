package prompts

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

var cachedDeclaredProfiles atomic.Pointer[[]sandbox.ToolProfile]

// DeclaredReader constructs a Reader for a shipped profile, agent, or surface
// without a session.
func DeclaredReader(catalog *extpacks.EffectiveCatalog, scopeID string) (Reader, error) {
	if catalog == nil {
		var err error
		catalog, err = extpacks.CatalogForConsumers()
		if err != nil {
			return Reader{}, err
		}
	}
	profiles, err := sandbox.LoadToolProfilesWithCatalog(catalog)
	if err != nil {
		return Reader{}, err
	}
	cloned := slices.Clone(profiles)
	cachedDeclaredProfiles.Store(&cloned)
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "-" {
		return Reader{}, nil
	}
	profileID := ""
	surfaceID := ""
	mode := ""
	var plan toolsurface.Plan
	switch {
	case scopeID == "" || scopeID == CoordinatorProfileID:
		profileID = CoordinatorProfileID
	case hasProfileID(profiles, scopeID):
		profileID = scopeID
	case IsCoordinatorSurfaceID(scopeID):
		profileID = CoordinatorProfileID
		surfaceID = scopeID
		plan, err = CompileSurfacePlan(surfaceID, 1)
		if err != nil {
			return Reader{}, err
		}
	default:
		if mapped, mapErr := ToolProfileForAgent(scopeID); mapErr == nil && hasProfileID(profiles, mapped) {
			profileID = mapped
		} else if agents, loadErr := agentdef.LoadEffectiveWithCatalog(catalog); loadErr == nil {
			for _, a := range agents {
				if a.ID == scopeID {
					profileID = strings.TrimSpace(a.ToolProfile)
					break
				}
			}
		}
		if profileID == "" {
			return Reader{}, fmt.Errorf("unknown reader scope %q", scopeID)
		}
	}
	schemas, _ := loadToolSchemaCached(catalog)
	return NewReader(ReaderInput{
		ProfileID: profileID,
		SurfaceID: surfaceID,
		Mode:      mode,
		Profiles:  profiles,
		Plan:      plan,
		Schemas:   schemas,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
}

func declaredToolProfiles(catalog *extpacks.EffectiveCatalog) ([]sandbox.ToolProfile, error) {
	if catalog == nil {
		if p := cachedDeclaredProfiles.Load(); p != nil {
			return slices.Clone(*p), nil
		}
	}
	if _, err := DeclaredReader(catalog, "-"); err != nil {
		return nil, err
	}
	if p := cachedDeclaredProfiles.Load(); p != nil {
		return slices.Clone(*p), nil
	}
	return nil, fmt.Errorf("tool profiles unavailable")
}

func hasProfileID(profiles []sandbox.ToolProfile, id string) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

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

	catalog, _ := extpacks.CatalogForConsumers()
	schemas := in.Schemas
	if schemas == nil && catalog != nil {
		schemas, _ = loadToolSchemaCached(catalog)
	}
	toolViews := make([]AgentToolArgView, 0, len(offered))
	for _, name := range offered {
		view := AgentToolArgView{Name: name}
		if schemas != nil {
			if meta, ok := schemas.ToolMeta(name); ok {
				view.RequiredArgs, _ = toolschema.ArgFieldSummary(meta.ArgsSchema)
			}
		}
		toolViews = append(toolViews, view)
	}
	roster, err := RequestableTools(requestable, schemas)
	if err != nil {
		return nil, err
	}
	var rejectCodes []AgentRejectCodeView
	if catalog != nil {
		if policy, err := hintregistry.ListEffectiveWithCatalog(catalog); err == nil {
			if hints, err := loadHintCodeRowsCached(catalog.Revision, policy); err == nil {
				rejectCodes = recoverableRejectCodes(hints, append(slices.Clone(offered), requestable...))
			}
		}
	}
	data := AgentToolSurfaceData{
		ToolProfile:     profileID,
		Tools:           toolViews,
		Requestable:     roster,
		RejectCodes:     rejectCodes,
		WriteGlobs:      slices.Clone(profile.WriteGlobs),
		CommandEffects:  effects,
		CommandDeferred: profile.ToolDeferred("command"),
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
