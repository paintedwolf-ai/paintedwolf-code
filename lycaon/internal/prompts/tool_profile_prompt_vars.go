package prompts

import (
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/toolschema"
)

var (
	surveyToolDisplayOrder      = []string{"command"}
	readContentToolDisplayOrder = []string{"command"}
	nativeToolFamiliesOnce      sync.Once
	nativeFilesystemTools       []string
	nativeTerminalTools         []string
	nativeMutationTools         []string
	nativeWriteTools            []string
	nativeSecretTools           []string
	nativeWebResearchTools      []string
	nativeProgressGatedTools    []string
	nativeOperationSet          map[string]bool
	commandGatedByOperations    bool
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

// HostRunnerToolOrder returns host runners in prompt order.
func HostRunnerToolOrder() []string {
	loadNativeToolFamilies()
	out := append([]string(nil), nativemanifest.ArgvHostRunnerTools()...)
	out = append(out, nativeTerminalTools...)
	return out
}

// PreferNativeFileToolOrder returns native.filesystem in manifest order.
func PreferNativeFileToolOrder() []string {
	loadNativeToolFamilies()
	return append([]string(nil), nativeFilesystemTools...)
}

func enabledToolSet(toolViews []AgentToolArgView) map[string]bool {
	out := make(map[string]bool, len(toolViews))
	for _, t := range toolViews {
		name := strings.TrimSpace(t.Name)
		if name != "" {
			out[name] = true
		}
	}
	return out
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

func enabledGitTools(enabled map[string]bool) []string {
	var out []string
	for name := range enabled {
		if strings.HasPrefix(name, "git_") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func profileConcurrentTools(toolViews []AgentToolArgView) []string {
	var out []string
	for _, t := range toolViews {
		if contract, ok := toolcontract.Lookup(t.Name); ok && contract.Concurrent() {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

func profileSerialTools(toolViews []AgentToolArgView) []string {
	loadNativeToolFamilies()
	enabled := enabledToolSet(toolViews)
	mutationOrder := append(append([]string(nil), nativeWriteTools...), "command")
	out := orderedEnabledTools(mutationOrder, enabled)
	for _, name := range enabledGitTools(enabled) {
		if name == "git_commit" || name == "git_restore" {
			continue
		}
		contract, declared := toolcontract.Lookup(name)
		if !slices.Contains(out, name) && (!declared || !contract.Concurrent()) {
			out = append(out, name)
		}
	}
	for _, t := range toolViews {
		contract, declared := toolcontract.Lookup(t.Name)
		if (declared && contract.Concurrent()) || orchestrationPromptTool(t.Name) {
			continue
		}
		if !slices.Contains(out, t.Name) {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

func orchestrationPromptTool(name string) bool {
	contract, ok := toolcontract.Lookup(strings.TrimSpace(strings.ToLower(name)))
	if !ok {
		return false
	}
	switch contract.Owner {
	case "delegation", "handoff", "workflow_state", "workflows", "workers", "worker_merge", "board", "loop_wake":
		return true
	default:
		return false
	}
}

func profileHasScanDrilldownTools(enabled map[string]bool) bool {
	return len(scanDrilldownToolsForCheck(enabled)) > 0
}

// VisibleToolsNativePartialVars returns surface capability flags.
func VisibleToolsNativePartialVars(visibleTools []string) map[string]any {
	views := make([]AgentToolArgView, 0, len(visibleTools))
	for _, name := range visibleTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		views = append(views, AgentToolArgView{Name: name})
	}
	return toolProfileCapabilityTemplateVars(AgentToolSurfaceData{Tools: views})
}

// MergeProfileRequestableVars sets profile_can_request for the tools profileID
// can still load when request_tools is offered.
func MergeProfileRequestableVars(profileID string, offered []string, into map[string]any) {
	if into == nil {
		return
	}
	into["profile_can_request"] = requestableToolSet(offered, profileAllowedToolNames(profileID))
}

func profileAllowedToolNames(profileID string) []string {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil
	}
	if profiles, err := sandbox.LoadToolProfiles(); err == nil {
		resolvedID := profileID
		if mapped, err := ToolProfileForAgent(profileID); err == nil {
			resolvedID = mapped
		}
		for _, p := range profiles {
			if p.ID != profileID && p.ID != resolvedID {
				continue
			}
			var out []string
			for _, name := range visibleToolNames(p, nil) {
				if p.ToolAllowed(name) {
					out = append(out, name)
				}
			}
			return out
		}
	}
	if IsCoordinatorSurfaceID(profileID) {
		if loadable, err := LoadCoordinatorSurfaceLoadable(profileID); err == nil {
			return loadable
		}
	}
	return nil
}

func toolProfileCapabilityTemplateVars(data AgentToolSurfaceData) map[string]any {
	loadNativeToolFamilies()
	enabled := enabledToolSet(data.Tools)
	surveyTools := orderedEnabledTools(surveyToolDisplayOrder, enabled)
	writeTools := orderedEnabledTools(nativeWriteTools, enabled)
	readTools := orderedEnabledTools(readContentToolDisplayOrder, enabled)
	concurrentTools := profileConcurrentTools(data.Tools)
	serialTools := profileSerialTools(data.Tools)
	preferNative := orderedEnabledTools(PreferNativeFileToolOrder(), enabled)
	hostRunner := orderedEnabledTools(HostRunnerToolOrder(), enabled)
	mutationCapable := ProfileMutationCapable(data)
	mutation, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	commandMutates := enabled["command"] && (data.CommandEffects == 0 || data.CommandEffects.Allows(mutation))
	if commandMutates {
		writeTools = append([]string{"command"}, writeTools...)
	}
	out := map[string]any{
		"profile_has_write_tools":         len(writeTools) > 0 || commandMutates,
		"profile_has_read_tools":          len(readTools) > 0,
		"profile_mutation_capable":        mutationCapable,
		"profile_command_mutation":        commandMutates,
		"command_output_files":            enabled["command"],
		"profile_can_request":             requestableToolSet(toolViewNames(data.Tools), requestableToolNames(data.Requestable)),
		"has_find_tool":                   enabled["find"],
		"native_edit_limit_mib":           readcaps.MaxMutationBytes >> 20,
		"profile_has_jq_edit":             commandMutates,
		"profile_has_retrieval":           profileHasRetrievalTool(enabled),
		"profile_has_terminal_capture":    commandMutates,
		"command_on_floor":                enabled["command"] && !data.CommandDeferred,
		"is_coordinator":                  data.ToolProfile == CoordinatorProfileID,
		"platform_os":                     runtime.GOOS,
		"reviews_confined_writes":         true,
		"command_external_hidden":         false,
		"command_external_shown":          true,
		"tool_check_write_tools":          writeTools,
		"tool_check_survey_tools":         surveyTools,
		"tool_check_verify_tools":         verifyToolsForCheck(enabled, commandMutates),
		"tool_check_scan_drilldown_tools": scanDrilldownToolsForCheck(enabled),
		"profile_concurrent_tools":        concurrentTools,
		"profile_serial_tools":            serialTools,
		"profile_git_tools":               enabledGitTools(enabled),
		"prefer_native_file_tools":        preferNative,
		"host_runner_tools":               hostRunner,
	}
	for _, name := range currentToolNames() {
		out["profile_has_"+name] = enabled[name]
	}
	offeredNames := toolViewNames(data.Tools)
	MergeRunnerCapabilityVars(offeredNames, out)
	MergeHTTPActionVars(offeredNames, out)
	mergeOfferedToolGuidance(offeredNames, out)
	out["more_tools_loadable"] = len(data.Requestable) > 0
	return out
}

// currentToolNames lists every tool declared by a stock profile or the manifest.
// Operations are excluded: agents cannot call them as tools, and the guidance
// facts that name operations are answered in mergeOfferedToolGuidance.
var currentToolNames = sync.OnceValue(func() []string {
	var names []string
	if cfg, err := nativemanifest.Load(); err == nil {
		names = append(names, cfg.AllTools()...)
		names = append(names, cfg.HostProducedTools...)
	}
	if profiles, err := sandbox.LoadToolProfiles(); err == nil {
		for _, profile := range profiles {
			for name := range profile.Tools {
				if toolschema.ValidToolName(name) {
					names = append(names, name)
				}
			}
		}
	}
	return names
})

// MergeRunnerCapabilityVars populates runner_<capability>_tools for offered tools,
// excluding command when command mutation is not allowed on the surface.
func MergeRunnerCapabilityVars(offered []string, vars map[string]any) {
	if vars == nil {
		return
	}
	commandMutates, ok := vars["profile_command_mutation"].(bool)
	if !ok {
		commandMutates = true
	}
	capabilities := map[string]toolcontract.Capability{
		"host_execution":   toolcontract.CapabilityHostExecution,
		"host_resource":    toolcontract.CapabilityHostResource,
		"socket":           toolcontract.CapabilitySocket,
		"direct_ip":        toolcontract.CapabilityDirectIP,
		"local_listen":     toolcontract.CapabilityLocalListen,
		"loopback_connect": toolcontract.CapabilityLoopbackConnect,
		"write_root":       toolcontract.CapabilityWriteRoot,
		"read_path":        toolcontract.CapabilityReadPath,
	}
	for name, capability := range capabilities {
		var tools []string
		for _, offeredTool := range offered {
			if offeredTool == "command" && !commandMutates {
				continue
			}
			if multiRoot, ok := toolcontract.MultiRootOf(offeredTool); !ok || multiRoot != toolcontract.MultiRootCommand {
				continue
			}
			contract, ok := toolcontract.Lookup(offeredTool)
			if ok && contract.Supports(capability) {
				tools = append(tools, offeredTool)
			}
		}
		vars["runner_"+name+"_tools"] = tools
	}
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
	if mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation); err == nil && data.CommandEffects != 0 && !data.CommandEffects.Allows(mutation) {
		delete(enabled, "command")
	}
	return toolSetMutationCapable(enabled)
}

// ToolNamesMutationCapable reports whether a tool set can change the project tree.
func ToolNamesMutationCapable(names []string) bool {
	return toolSetMutationCapable(toolNameSet(names))
}

// CoordinatorSurfaceMutationCapable reports whether a coordinator surface can mutate the tree.
func CoordinatorSurfaceMutationCapable(surfaceID string) bool {
	row, err := loadCoordinatorSurfaceRow(surfaceID)
	if err != nil {
		return false
	}
	enabled := toolNameSet(append(append([]string(nil), row.Floor...), row.LoadableTools()...))
	if effects, err := toolcontract.NewCommandEffects(row.CommandEffects...); err == nil && effects != 0 {
		if mutation, mErr := toolcontract.NewCommandEffects(toolcontract.CommandMutation); mErr == nil && !effects.Allows(mutation) {
			delete(enabled, "command")
		}
	}
	return toolSetMutationCapable(enabled)
}

// CoordinatorSurfaceAllowsCommandMutation reports whether a coordinator surface
// allows command to perform mutations or terminal capture.
func CoordinatorSurfaceAllowsCommandMutation(surfaceID string) bool {
	row, err := loadCoordinatorSurfaceRow(surfaceID)
	if err != nil {
		return false
	}
	effects, err := toolcontract.NewCommandEffects(row.CommandEffects...)
	if err != nil || effects == 0 {
		return true
	}
	mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	return err == nil && effects.Allows(mutation)
}

// IsCoordinatorSurfaceID reports whether id is a declared coordinator turn surface.
func IsCoordinatorSurfaceID(id string) bool {
	_, err := loadCoordinatorSurfaceRow(id)
	return err == nil
}

// ScopeAllowsCommandMutation reports whether a profile ID, agent ID, or
// coordinator surface ID allows command to perform mutations or external execution.
func ScopeAllowsCommandMutation(scopeID string) bool {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" || scopeID == CoordinatorProfileID {
		return true
	}
	if IsCoordinatorSurfaceID(scopeID) {
		return CoordinatorSurfaceAllowsCommandMutation(scopeID)
	}
	if profID, err := ToolProfileForAgent(scopeID); err == nil && profID != "" {
		scopeID = profID
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return true
	}
	for _, p := range profiles {
		if p.ID == scopeID {
			if p.CommandEffects == 0 {
				return true
			}
			mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
			return err == nil && p.CommandEffects.Allows(mutation)
		}
	}
	return true
}

// ApplyReadOnlyCommandEffects bounds mutation capability and runner vars when a
// profile or surface restricts command_effects to read_only.
func ApplyReadOnlyCommandEffects(offered, requestable []string, into map[string]any) {
	if into == nil {
		return
	}
	loadNativeToolFamilies()
	enabled := toolNameSet(append(append([]string(nil), offered...), requestable...))
	delete(enabled, "command")
	offeredSet := toolNameSet(offered)
	into["profile_mutation_capable"] = toolSetMutationCapable(enabled)
	into["profile_command_mutation"] = false
	into["profile_has_code_rewrite"] = false
	into["profile_has_jq_edit"] = false
	writeTools := orderedEnabledTools(nativeWriteTools, offeredSet)
	into["profile_has_write_tools"] = len(writeTools) > 0
	into["tool_check_write_tools"] = writeTools
	into["progress_gated_tools"] = progressGatedOfferedTools(offeredSet, false)
	into["profile_has_terminal_capture"] = false
	into["tool_check_verify_tools"] = verifyToolsForCheck(offeredSet, false)
	MergeRunnerCapabilityVars(offered, into)
}

// ApplyCoordinatorSurfaceCommandEffects bounds mutation capability flags when a
// coordinator surface restricts command_effects to read_only.
func ApplyCoordinatorSurfaceCommandEffects(surfaceID string, offered, requestable []string, into map[string]any) {
	if into == nil || CoordinatorSurfaceAllowsCommandMutation(surfaceID) {
		return
	}
	ApplyReadOnlyCommandEffects(offered, requestable, into)
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
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return AgentToolSurfaceData{}, nil, "", false
	}
	surface, err := LoadAgentToolSurface(profileID, nil, nil, SurfaceTurn{}, profiles)
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
	surface, err := LoadAgentToolSurface(profile.ID, nil, nil, SurfaceTurn{}, []sandbox.ToolProfile{profile})
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

func verifyToolsForCheck(enabled map[string]bool, commandMutates bool) []string {
	var out []string
	if commandMutates {
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

func mergeOfferedToolGuidance(offered []string, into map[string]any) {
	loadNativeToolFamilies()
	set := toolNameSet(offered)
	commandMutates, ok := into["profile_command_mutation"].(bool)
	if !ok {
		commandMutates = set["command"]
	}
	into["progress_gated_tools"] = progressGatedOfferedTools(set, commandMutates)
	into["profile_has_page_controls"] = hasToolFamily(set, "page")
	into["profile_has_scan_drilldown"] = profileHasScanDrilldownTools(set)
	into["profile_has_managed_secrets"] = len(nativeSecretTools) > 0 && allPresent(nativeSecretTools, set)
	into["command_output_files"] = set["command"]
	// Guidance facts must all be present in captured vars. Operations are not
	// tools, so answer the fact keys that name manifest operations unless an
	// explicit capability var already answered them.
	for _, key := range CommandGuidanceFactKeys() {
		name, ok := strings.CutPrefix(key, "profile_has_")
		if !ok || !nativeOperationSet[name] {
			continue
		}
		if _, answered := into[key]; answered {
			continue
		}
		into[key] = set[name]
	}
}
