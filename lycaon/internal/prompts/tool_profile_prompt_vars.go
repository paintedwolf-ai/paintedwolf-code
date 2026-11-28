package prompts

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
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
	nativeMutationTools      []string
	nativeWriteTools         []string
	nativeSecretTools        []string
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
			if contract.Owner == "secrets" {
				nativeSecretTools = append(nativeSecretTools, name)
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

var (
	gitReadRequiredForms = []string{
		"git status",
		"git diff",
		"git log",
		"git show",
		"git blame",
		"git rev-parse",
		"git rev-list",
		"git branch",
		"git grep",
		"git describe",
		"git reflog",
		"git ls-tree",
		"git cat-file",
	}
	gitCommitRequiredForms = []string{
		"git status",
		"git diff",
		"git log",
		"git add",
		"git commit",
	}
	gitBisectRequiredForms = []string{
		"git status",
		"git diff",
		"git bisect",
		"git bisect start",
		"git bisect run",
		"git bisect log",
		"git bisect reset",
		"git worktree",
	}
)

func holdsAllCommandPrograms(held map[string]bool, programs ...string) bool {
	if !held["command"] {
		return false
	}
	for _, p := range programs {
		if !toolcontract.CommandLanguageReachable(p, held) {
			return false
		}
	}
	return true
}

func deriveOfferedGitFacts(
	in HostFacts,
	hasAnyGit bool,
	held map[string]bool,
) (canGitRead, canAnyGitRead, canGitCommit, canGitBisect bool) {
	if in.OnWorkerBranch {
		return false, false, false, false
	}
	readFormsReachable := holdsAllCommandPrograms(held, gitReadRequiredForms...)
	canGitRead = in.HasGit && readFormsReachable
	canAnyGitRead = hasAnyGit && readFormsReachable
	if !in.HasGit {
		return canGitRead, canAnyGitRead, false, false
	}
	canGitCommit = holdsAllCommandPrograms(held, gitCommitRequiredForms...)
	canGitBisect = held[toolcontract.ReachCommandExternalExec] && holdsAllCommandPrograms(held, gitBisectRequiredForms...)
	return canGitRead, canAnyGitRead, canGitCommit, canGitBisect
}

func deriveLoadableGitFacts(
	in HostFacts,
	canRequestCommand, canGitRead, canGitCommit, canGitBisect bool,
	loadableHeld map[string]bool,
) (canRequestGitRead, canRequestGitCommit, canRequestGitBisect bool) {
	if !in.HasGit || in.OnWorkerBranch || !canRequestCommand {
		return false, false, false
	}
	canRequestGitRead = !canGitRead && holdsAllCommandPrograms(loadableHeld, gitReadRequiredForms...)
	canRequestGitCommit = !canGitCommit && holdsAllCommandPrograms(loadableHeld, gitCommitRequiredForms...)
	canRequestGitBisect = !canGitBisect &&
		loadableHeld[toolcontract.ReachCommandExternalExec] &&
		holdsAllCommandPrograms(loadableHeld, gitBisectRequiredForms...)
	return canRequestGitRead, canRequestGitCommit, canRequestGitBisect
}

func resolveProjectedToolSchemas(
	projected map[string]map[string]any,
	profile sandbox.ToolProfile,
	offered []string,
	schemas *toolschema.Config,
	commandExternalExec bool,
	askMode bool,
) map[string]map[string]any {
	out := make(map[string]map[string]any, len(offered))
	// An explicitly supplied projection map freezes tool schemas; missing entries
	// certify zero schema fields instead of falling back to the raw catalog.
	if projected != nil {
		for _, name := range offered {
			if explicit, ok := projected[name]; ok && explicit != nil {
				out[name] = explicit
			}
		}
		return out
	}
	for _, name := range offered {
		if schemas == nil {
			continue
		}
		meta, ok := schemas.ToolMeta(name)
		if !ok {
			continue
		}
		out[name] = toolschema.ProjectToolArgsSchema(name, meta.ArgsSchema, toolschema.ProjectOptions{
			CommandReadOnly:     name == "command" && !commandExternalExec,
			PruneWaitConditions: name == "wait",
			WaitConditions:      profile.WaitConditions,
			AskMode:             askMode,
		})
	}
	return out
}

func runnerCapabilityTools(
	offered []string,
	projectedSchemas map[string]map[string]any,
	explicitProjected bool,
	cap toolcontract.Capability,
	fieldPath string,
	commandAllowed bool,
) []string {
	var tools []string
	for _, offeredTool := range offered {
		if offeredTool == "command" && !commandAllowed {
			continue
		}
		if multiRoot, ok := toolcontract.MultiRootOf(offeredTool); !ok || multiRoot != toolcontract.MultiRootCommand {
			continue
		}
		contract, ok := toolcontract.Lookup(offeredTool)
		if !ok || !contract.Supports(cap) {
			continue
		}
		if fieldPath != "" {
			if explicitProjected {
				proj, hasProj := projectedSchemas[offeredTool]
				if !hasProj || !toolschema.SchemaAcceptsField(proj, fieldPath) {
					continue
				}
			} else if len(projectedSchemas) > 0 {
				if proj, hasProj := projectedSchemas[offeredTool]; hasProj && !toolschema.SchemaAcceptsField(proj, fieldPath) {
					continue
				}
			}
		}
		tools = append(tools, offeredTool)
	}
	return tools
}

func projectedSchemaTools(
	offered []string,
	projectedSchemas map[string]map[string]any,
	fieldPath string,
	runnerOnly bool,
) []string {
	if len(projectedSchemas) == 0 {
		return nil
	}
	var out []string
	for _, offeredTool := range offered {
		if runnerOnly {
			if multiRoot, ok := toolcontract.MultiRootOf(offeredTool); !ok || multiRoot != toolcontract.MultiRootCommand {
				continue
			}
		}
		proj, ok := projectedSchemas[offeredTool]
		if !ok {
			continue
		}
		if toolschema.SchemaAcceptsField(proj, fieldPath) {
			out = append(out, offeredTool)
		}
	}
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
	surface, err := LoadAgentToolSurface(profileID, nil, nil, nil, nil, profiles)
	if err != nil {
		return AgentToolSurfaceData{}, profiles, profileID, false
	}
	return surface, profiles, profileID, true
}

type agentSurfaceCheckEntry struct {
	mutationCapable    bool
	surveysProjectTree bool
	ok                 bool
}

var (
	agentSurfaceCheckMu    sync.RWMutex
	agentSurfaceCheckCache = map[string]agentSurfaceCheckEntry{}
)

func cachedAgentSurfaceChecks(agentType string) agentSurfaceCheckEntry {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return agentSurfaceCheckEntry{}
	}
	catalog, _ := extpacks.CatalogForConsumers()
	key := catalogRevisionKey(catalog) + "\x00" + agentType
	agentSurfaceCheckMu.RLock()
	if entry, found := agentSurfaceCheckCache[key]; found {
		agentSurfaceCheckMu.RUnlock()
		return entry
	}
	agentSurfaceCheckMu.RUnlock()

	surface, _, _, ok := loadAgentSurfaceForCheck(agentType)
	entry := agentSurfaceCheckEntry{ok: ok}
	if ok {
		entry.mutationCapable = ProfileMutationCapable(surface)
		entry.surveysProjectTree = ProfileSurveysProjectTree(surface)
	}
	agentSurfaceCheckMu.Lock()
	if len(agentSurfaceCheckCache) >= 256 {
		agentSurfaceCheckCache = map[string]agentSurfaceCheckEntry{}
	}
	agentSurfaceCheckCache[key] = entry
	agentSurfaceCheckMu.Unlock()
	return entry
}

// AgentMutationCapable resolves an agent's mutation capability.
func AgentMutationCapable(agentType string) (capable bool, ok bool) {
	entry := cachedAgentSurfaceChecks(agentType)
	return entry.mutationCapable, entry.ok
}

// ToolProfileMutationCapable reports ProfileMutationCapable for one tool profile.
func ToolProfileMutationCapable(profile sandbox.ToolProfile) bool {
	if strings.TrimSpace(profile.ID) == "" {
		return false
	}
	surface, err := LoadAgentToolSurface(profile.ID, nil, nil, nil, nil, []sandbox.ToolProfile{profile})
	return err == nil && ProfileMutationCapable(surface)
}

// ProfileSurveysProjectTree reports whether a surface can survey the tree.
func ProfileSurveysProjectTree(data AgentToolSurfaceData) bool {
	enabled := toolNameSet(append(toolViewNames(data.Tools), requestableToolNames(data.Requestable)...))
	return len(orderedEnabledTools(surveyToolDisplayOrder, enabled)) > 0
}

// AgentSurveysProjectTree resolves an agent's tree-survey capability.
func AgentSurveysProjectTree(agentType string) (surveys bool, ok bool) {
	entry := cachedAgentSurfaceChecks(agentType)
	return entry.surveysProjectTree, entry.ok
}

// AgentRunsWithoutWorkspace accepts agents with no tree capability.
func AgentRunsWithoutWorkspace(agentType string) bool {
	entry := cachedAgentSurfaceChecks(agentType)
	return !entry.ok || (!entry.mutationCapable && !entry.surveysProjectTree)
}

// KeepSpawnAgentOnEmptyRepo filters survey-only workers from empty projects.
func KeepSpawnAgentOnEmptyRepo(agentType string) bool {
	entry := cachedAgentSurfaceChecks(agentType)
	return !entry.ok || entry.mutationCapable || !entry.surveysProjectTree
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
