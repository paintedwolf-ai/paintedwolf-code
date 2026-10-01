package prompts

import (
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

var (
	surveyToolDisplayOrder = []string{
		"summarize", "survey_repo", "find", "grep", "read", "list_dir", "stat", "wc", "jq",
		"git_status", "git_diff", "git_log", "git_show", "git_blame", "git_ref", "git_branches",
	}
	writeToolDisplayOrder = []string{
		"write", "edit", "replace_lines", "code_rewrite", "jq_edit",
	}
	// Content readers that can pull secret values into the transcript.
	readContentToolDisplayOrder = []string{
		"read", "grep", "jq", "summarize",
	}
	mutationToolDisplayOrder = []string{
		"write", "edit", "replace_lines", "code_rewrite", "chmod", "delete",
		"git_commit", "git_restore", "command",
	}
	nativeToolFamiliesOnce sync.Once
	nativeFilesystemTools  []string
	nativeTerminalTools    []string
	nativeMutationTools    []string
)

func loadNativeToolFamilies() {
	nativeToolFamiliesOnce.Do(func() {
		cfg, err := nativemanifest.Load()
		if err != nil {
			return
		}
		nativeFilesystemTools = append([]string(nil), cfg.Group("filesystem")...)
		nativeTerminalTools = append([]string(nil), cfg.Group("terminal")...)
		// Worker-branch tools define mutation capability.
		nativeMutationTools = cfg.WorkerBranchTools()
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
	enabled := enabledToolSet(toolViews)
	out := orderedEnabledTools(mutationToolDisplayOrder, enabled)
	for _, name := range enabledGitTools(enabled) {
		if name == "git_commit" || name == "git_restore" {
			continue
		}
		found := false
		for _, existing := range out {
			if existing == name {
				found = true
				break
			}
		}
		contract, declared := toolcontract.Lookup(name)
		if !found && (!declared || !contract.Concurrent()) {
			out = append(out, name)
		}
	}
	// Append remaining serial tools in manifest order.
	for _, t := range toolViews {
		contract, declared := toolcontract.Lookup(t.Name)
		if (declared && contract.Concurrent()) || orchestrationPromptTool(t.Name) {
			continue
		}
		found := false
		for _, existing := range out {
			if existing == t.Name {
				found = true
				break
			}
		}
		if !found {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

func orchestrationPromptTool(name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	switch {
	case strings.HasPrefix(name, "handoff_"),
		strings.HasPrefix(name, "delegate_"),
		strings.HasPrefix(name, "state_"),
		strings.HasPrefix(name, "workflow_"),
		name == "task",
		name == "wait",
		name == "promote_overlay",
		name == "reject_overlay",
		name == "preview_overlay",
		name == "worker_cancel",
		name == "pack_board":
		return true
	default:
		return false
	}
}

func profileHasScanDrilldownTools(enabled map[string]bool) bool {
	for _, name := range []string{"scan_list", "scan_summary", "scan_query"} {
		if enabled[name] {
			return true
		}
	}
	return false
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

func toolProfileCapabilityTemplateVars(data AgentToolSurfaceData) map[string]any {
	enabled := enabledToolSet(data.Tools)
	surveyTools := orderedEnabledTools(surveyToolDisplayOrder, enabled)
	writeTools := orderedEnabledTools(writeToolDisplayOrder, enabled)
	readTools := orderedEnabledTools(readContentToolDisplayOrder, enabled)
	concurrentTools := profileConcurrentTools(data.Tools)
	serialTools := profileSerialTools(data.Tools)
	preferNative := orderedEnabledTools(PreferNativeFileToolOrder(), enabled)
	hostRunner := orderedEnabledTools(HostRunnerToolOrder(), enabled)
	out := map[string]any{
		"profile_has_write_tools":         len(writeTools) > 0,
		"profile_has_read_tools":          len(readTools) > 0,
		"profile_mutation_capable":        ProfileMutationCapable(data),
		"profile_has_command":             enabled["command"],
		"profile_has_task":                enabled["task"],
		"profile_has_wait":                enabled["wait"],
		"profile_has_verify":              enabled["verify"],
		"has_find_tool":                   enabled["find"],
		"profile_has_git_commit":          enabled["git_commit"],
		"profile_has_grep":                enabled["grep"],
		"profile_has_code_rewrite":        enabled["code_rewrite"],
		"native_edit_limit_mib":           readcaps.MaxMutationBytes >> 20,
		"profile_has_list_dir":            enabled["list_dir"],
		"profile_has_read":                enabled["read"],
		"profile_has_jq":                  enabled["jq"],
		"profile_has_jq_edit":             enabled["jq_edit"],
		"profile_has_survey_repo":         enabled["survey_repo"],
		"profile_has_summarize":           enabled["summarize"],
		"profile_has_view_image":          enabled["view_image"],
		"profile_has_view_video":          enabled["view_video"],
		"profile_has_retrieval":           profileHasRetrievalTool(enabled),
		"profile_has_skills_read":         enabled["skills_read"],
		"profile_has_ask_user":            enabled["ask_user"],
		"profile_has_surface_note":        enabled["surface_note"],
		"profile_has_recall":              enabled["recall"],
		"profile_has_terminal_open":       enabled["terminal_open"],
		"profile_has_terminal_send":       enabled["terminal_send"],
		"profile_has_terminal_snapshot":   enabled["terminal_snapshot"],
		"profile_has_terminal_capture":    enabled["command"],
		"tool_check_write_tools":          writeTools,
		"tool_check_survey_tools":         surveyTools,
		"tool_check_verify_tools":         verifyToolsForCheck(enabled),
		"tool_check_scan_drilldown_tools": scanDrilldownToolsForCheck(enabled),
		"profile_concurrent_tools":        concurrentTools,
		"profile_serial_tools":            serialTools,
		"profile_git_tools":               enabledGitTools(enabled),
		"prefer_native_file_tools":        preferNative,
		"host_runner_tools":               hostRunner,
	}
	MergeHTTPActionVars(toolViewNames(data.Tools), out)
	mergeOfferedToolGuidance(toolViewNames(data.Tools), out)
	out["more_tools_loadable"] = len(data.Requestable) > 0
	return out
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
	enabled := enabledToolSet(data.Tools)
	for _, t := range data.Requestable {
		name := strings.TrimSpace(t.Name)
		if name != "" {
			enabled[name] = true
		}
	}
	return toolSetMutationCapable(enabled)
}

// ToolNamesMutationCapable reports whether a tool set can change the project tree.
func ToolNamesMutationCapable(names []string) bool {
	enabled := make(map[string]bool, len(names))
	for _, name := range names {
		enabled[strings.TrimSpace(name)] = true
	}
	return toolSetMutationCapable(enabled)
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

// AgentMutationCapable resolves an agent's mutation capability.
func AgentMutationCapable(agentType string) (capable bool, ok bool) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return false, false
	}
	profileID, err := ToolProfileForAgent(agentType)
	if err != nil {
		return false, false
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return false, false
	}
	surface, err := LoadAgentToolSurface(profileID, nil, nil, SurfaceTurn{}, profiles)
	if err != nil {
		return false, false
	}
	capable = ProfileMutationCapable(surface)
	return capable, true
}

// ToolProfileMutationCapable reports ProfileMutationCapable for one tool profile.
func ToolProfileMutationCapable(profile sandbox.ToolProfile) bool {
	if strings.TrimSpace(profile.ID) == "" {
		return false
	}
	surface, err := LoadAgentToolSurface(profile.ID, nil, nil, SurfaceTurn{}, []sandbox.ToolProfile{profile})
	if err != nil {
		return false
	}
	return ProfileMutationCapable(surface)
}

// ProfileSurveysProjectTree reports whether a surface can survey the tree.
func ProfileSurveysProjectTree(data AgentToolSurfaceData) bool {
	enabled := enabledToolSet(data.Tools)
	for _, t := range data.Requestable {
		name := strings.TrimSpace(t.Name)
		if name != "" {
			enabled[name] = true
		}
	}
	return len(orderedEnabledTools(surveyToolDisplayOrder, enabled)) > 0
}

// AgentSurveysProjectTree resolves an agent's tree-survey capability.
func AgentSurveysProjectTree(agentType string) (surveys bool, ok bool) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return false, false
	}
	profileID, err := ToolProfileForAgent(agentType)
	if err != nil {
		return false, false
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return false, false
	}
	surface, err := LoadAgentToolSurface(profileID, nil, nil, SurfaceTurn{}, profiles)
	if err != nil {
		return false, false
	}
	surveys = ProfileSurveysProjectTree(surface)
	return surveys, true
}

// AgentRunsWithoutWorkspace accepts agents with no tree capability.
func AgentRunsWithoutWorkspace(agentType string) bool {
	capable, okCap := AgentMutationCapable(agentType)
	surveys, okSur := AgentSurveysProjectTree(agentType)
	if !okCap || !okSur {
		return true
	}
	return !capable && !surveys
}

// KeepSpawnAgentOnEmptyRepo filters survey-only workers from empty projects.
func KeepSpawnAgentOnEmptyRepo(agentType string) bool {
	capable, okCap := AgentMutationCapable(agentType)
	surveys, okSur := AgentSurveysProjectTree(agentType)
	if !okCap || !okSur {
		return true
	}
	if capable {
		return true
	}
	return !surveys
}

func verifyToolsForCheck(enabled map[string]bool) []string {
	var out []string
	if enabled["command"] {
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

// urlFetchTools identify profiles that can observe URLs.
var urlFetchTools = []string{"fetch_url", "web_search"}

// AgentFetchesURLs resolves an agent's URL retrieval capability.
func AgentFetchesURLs(agentType string) (fetches bool, ok bool) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return false, false
	}
	profileID, err := ToolProfileForAgent(agentType)
	if err != nil {
		return false, false
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return false, false
	}
	for _, p := range profiles {
		if p.ID != profileID {
			continue
		}
		for _, tool := range urlFetchTools {
			if p.ToolAllowed(tool) {
				fetches = true
				break
			}
		}
		return fetches, true
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

// Capability guidance follows the offered schemas.
func mergeOfferedToolGuidance(offered []string, into map[string]any) {
	set := toolNameSet(offered)
	for _, name := range []string{"render_view", "capture_page", "measure_page", "page_open", "page_snapshot", "page_act", "page_close", "view_image", "view_video"} {
		into["profile_has_"+name] = set[name]
	}
	into["profile_has_page_controls"] = set["page_open"] || set["page_snapshot"] || set["page_act"] || set["page_close"]
	into["profile_has_scan_drilldown"] = profileHasScanDrilldownTools(set)
	available := true
	for _, name := range []string{"secret_generate", "secret_list", "secret_revoke"} {
		available = available && set[name]
	}
	into["profile_has_managed_secrets"] = available
}
