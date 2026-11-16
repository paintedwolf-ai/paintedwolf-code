package prompts

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

var (
	surveyToolDisplayOrder   = []string{"command"}
	nativeToolFamiliesOnce   sync.Once
	nativeFilesystemTools    []string
	nativeTerminalTools      []string
	nativeMutationTools      []string
	nativeWriteTools         []string
	nativeSecretTools        []string
	nativeWebResearchTools   []string
	nativeProgressGatedTools []string
	nativeOperationSet       map[string]bool
	commandGatedByOperations bool
)

func loadNativeToolFamilies() {
	nativeToolFamiliesOnce.Do(func() {
		cfg, err := nativemanifest.Load()
		if err != nil {
			return
		}
		nativeOperationSet = make(map[string]bool, len(cfg.AllOperations()))
		for _, name := range cfg.AllOperations() {
			nativeOperationSet[name] = true
		}
		nativeFilesystemTools = append(append([]string(nil), cfg.Group("filesystem")...), cfg.OperationGroup("filesystem")...)
		nativeTerminalTools = append([]string(nil), cfg.Group("terminal")...)
		nativeMutationTools = cfg.WorkerBranchTools()
		for _, name := range cfg.Group("filesystem") {
			if !toolcontract.CommandFormTool(name) {
				nativeWriteTools = append(nativeWriteTools, name)
			}
		}
		for _, name := range cfg.AllTools() {
			contract, ok := toolcontract.Lookup(name)
			if !ok {
				continue
			}
			switch contract.Owner {
			case "secrets":
				nativeSecretTools = append(nativeSecretTools, name)
			case "web_research":
				nativeWebResearchTools = append(nativeWebResearchTools, name)
			}
		}
		for _, name := range progress.GatedToolOrder() {
			if cfg.HasOperation(name) {
				commandGatedByOperations = true
				continue
			}
			nativeProgressGatedTools = append(nativeProgressGatedTools, name)
		}
	})
}

func orderedEnabledTools(order []string, enabled map[string]bool) []string {
	var out []string
	for _, name := range order {
		if enabled[name] {
			out = append(out, name)
		}
	}
	return out
}

func profileHasScanDrilldownTools(enabled map[string]bool) bool {
	return len(scanDrilldownToolsForCheck(enabled)) > 0
}

var (
	currentToolNamesOnce sync.Once
	currentToolNamesList []string
)

// currentToolNames lists every tool declared by a stock profile or the manifest.
// Operations are excluded: agents cannot call them as tools.
func currentToolNames() []string {
	currentToolNamesOnce.Do(func() {
		var names []string
		if cfg, err := nativemanifest.Load(); err == nil {
			names = append(names, cfg.AllTools()...)
			names = append(names, cfg.HostProducedTools...)
		}
		if profiles, err := declaredToolProfiles(nil); err == nil {
			for _, profile := range profiles {
				for name := range profile.Tools {
					if toolschema.ValidToolName(name) {
						names = append(names, name)
					}
				}
			}
		}
		currentToolNamesList = names
	})
	return currentToolNamesList
}

func runnerCapabilityTools(offered []string, cap toolcontract.Capability, commandAllowed bool) []string {
	var tools []string
	for _, offeredTool := range offered {
		if offeredTool == "command" && !commandAllowed {
			continue
		}
		if multiRoot, ok := toolcontract.MultiRootOf(offeredTool); !ok || multiRoot != toolcontract.MultiRootCommand {
			continue
		}
		contract, ok := toolcontract.Lookup(offeredTool)
		if ok && contract.Supports(cap) {
			tools = append(tools, offeredTool)
		}
	}
	return tools
}

func toolViewNames(views []AgentToolArgView) []string {
	out := make([]string, 0, len(views))
	for _, view := range views {
		out = append(out, view.Name)
	}
	return out
}

// ProfileMutationCapable reports whether a surface can change the tree.
func ProfileMutationCapable(data AgentToolSurfaceData) bool {
	enabled := toolNameSet(append(toolViewNames(data.Tools), requestableToolNames(data.Requestable)...))
	if !commandEffectFlags(true, data.CommandEffects).mutation {
		delete(enabled, "command")
	}
	return toolSetMutationCapable(enabled)
}

// IsCoordinatorSurfaceID reports whether id is a declared coordinator turn surface.
func IsCoordinatorSurfaceID(id string) bool {
	_, err := loadCoordinatorSurfaceRow(id)
	return err == nil
}

func toolSetMutationCapable(enabled map[string]bool) bool {
	loadNativeToolFamilies()
	for _, name := range nativeMutationTools {
		if enabled[name] {
			return true
		}
	}
	return false
}

func loadAgentSurfaceForCheck(agentType string) (AgentToolSurfaceData, []sandbox.ToolProfile, string, bool) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return AgentToolSurfaceData{}, nil, "", false
	}
	profileID, err := ToolProfileForAgent(agentType)
	if err != nil {
		return AgentToolSurfaceData{}, nil, "", false
	}
	profiles, err := declaredToolProfiles(nil)
	if err != nil {
		return AgentToolSurfaceData{}, nil, profileID, false
	}
	surface, err := LoadAgentToolSurface(profileID, nil, nil, ReaderInput{}, profiles)
	if err != nil {
		return AgentToolSurfaceData{}, profiles, profileID, false
	}
	return surface, profiles, profileID, true
}

// AgentMutationCapable resolves an agent's mutation capability.
func AgentMutationCapable(agentType string) (capable bool, ok bool) {
	surface, _, _, ok := loadAgentSurfaceForCheck(agentType)
	if !ok {
		return false, false
	}
	return ProfileMutationCapable(surface), true
}

// ToolProfileMutationCapable reports ProfileMutationCapable for one tool profile.
func ToolProfileMutationCapable(profile sandbox.ToolProfile) bool {
	if strings.TrimSpace(profile.ID) == "" {
		return false
	}
	surface, err := LoadAgentToolSurface(profile.ID, nil, nil, ReaderInput{}, []sandbox.ToolProfile{profile})
	return err == nil && ProfileMutationCapable(surface)
}

// ProfileSurveysProjectTree reports whether a surface can survey the tree.
func ProfileSurveysProjectTree(data AgentToolSurfaceData) bool {
	enabled := toolNameSet(append(toolViewNames(data.Tools), requestableToolNames(data.Requestable)...))
	return len(orderedEnabledTools(surveyToolDisplayOrder, enabled)) > 0
}

// AgentSurveysProjectTree resolves an agent's tree-survey capability.
func AgentSurveysProjectTree(agentType string) (surveys bool, ok bool) {
	surface, _, _, ok := loadAgentSurfaceForCheck(agentType)
	if !ok {
		return false, false
	}
	return ProfileSurveysProjectTree(surface), true
}

// AgentRunsWithoutWorkspace accepts agents with no tree capability.
func AgentRunsWithoutWorkspace(agentType string) bool {
	capable, okCap := AgentMutationCapable(agentType)
	surveys, okSur := AgentSurveysProjectTree(agentType)
	return !okCap || !okSur || (!capable && !surveys)
}

// KeepSpawnAgentOnEmptyRepo filters survey-only workers from empty projects.
func KeepSpawnAgentOnEmptyRepo(agentType string) bool {
	capable, okCap := AgentMutationCapable(agentType)
	surveys, okSur := AgentSurveysProjectTree(agentType)
	return !okCap || !okSur || capable || !surveys
}

func verifyToolsForCheck(enabled map[string]bool, commandRuns bool) []string {
	var out []string
	if commandRuns {
		out = append(out, "command")
	}
	if enabled["verify"] {
		out = append(out, "verify")
	}
	return out
}

func scanDrilldownToolsForCheck(enabled map[string]bool) []string {
	var out []string
	for _, name := range []string{"scan_list", "scan_summary", "scan_query"} {
		if enabled[name] {
			out = append(out, name)
		}
	}
	return out
}

// AgentFetchesURLs resolves an agent's URL retrieval capability.
func AgentFetchesURLs(agentType string) (fetches bool, ok bool) {
	loadNativeToolFamilies()
	_, profiles, profileID, _ := loadAgentSurfaceForCheck(agentType)
	if profileID == "" {
		return false, false
	}
	for _, p := range profiles {
		if p.ID != profileID {
			continue
		}
		for _, tool := range nativeWebResearchTools {
			if p.ToolAllowed(tool) {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

// profileHasRetrievalTool detects tools that can import content.
func profileHasRetrievalTool(enabled map[string]bool) bool {
	for name, on := range enabled {
		if on && ingestion.IsRetrievalTool(name) {
			return true
		}
	}
	return false
}

func progressGatedOfferedTools(enabled map[string]bool, commandMutates bool) []string {
	loadNativeToolFamilies()
	var out []string
	if commandMutates && commandGatedByOperations && enabled["command"] {
		out = append(out, "command")
	}
	return append(out, orderedEnabledTools(nativeProgressGatedTools, enabled)...)
}

func hasToolFamily(enabled map[string]bool, family string) bool {
	for name, on := range enabled {
		if on {
			if got, ok := toolcontract.FamilyOf(name); ok && got == family {
				return true
			}
		}
	}
	return false
}
