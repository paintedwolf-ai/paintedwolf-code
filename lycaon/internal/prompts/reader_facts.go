package prompts

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
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
	profiles, err := toolProfilesCache.get(catalogCacheKey(catalog), func(string) ([]sandbox.ToolProfile, error) {
		return sandbox.LoadToolProfilesWithCatalog(catalog)
	})
	if err != nil {
		return Reader{}, err
	}
	profiles = slices.Clone(profiles)
	cachedDeclaredProfiles.Store(&profiles)
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
		mode = defaultExecutionModeForSurface(surfaceID)
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
	var spawnAllowlist []string
	if surfaceID != "" || profileID == CoordinatorProfileID {
		spawnAllowlist = spawn.AmbientAllowedAgents()
	}
	return NewReader(ReaderInput{
		ProfileID:      profileID,
		SurfaceID:      surfaceID,
		Mode:           mode,
		Profiles:       profiles,
		Plan:           plan,
		Schemas:        schemas,
		RootCount:      1,
		HasGit:         true,
		WebSearch:      true,
		SpawnAllowlist: spawnAllowlist,
	})
}

func declaredToolProfiles(catalog *extpacks.EffectiveCatalog) ([]sandbox.ToolProfile, error) {
	if _, err := DeclaredReader(catalog, "-"); err != nil {
		return nil, err
	}
	return slices.Clone(*cachedDeclaredProfiles.Load()), nil
}

func hasProfileID(profiles []sandbox.ToolProfile, id string) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

// IsProjectFileTool reports whether name is a project-file read/write/search
// tool or command, excluding non-file tools such as secret_list, terminal_open,
// and page_open.
func IsProjectFileTool(name string) bool {
	name = strings.TrimSpace(name)
	if name == "command" {
		return true
	}
	contract, ok := toolcontract.Lookup(name)
	if !ok {
		return false
	}
	switch contract.Owner {
	case "filesystem", "source_ledger", "survey":
		return true
	default:
		return false
	}
}

func hasOfferedProjectFileTool(offeredSet map[string]bool) bool {
	for name, ok := range offeredSet {
		if ok && IsProjectFileTool(name) {
			return true
		}
	}
	return false
}

func deriveSpawnFactFlags(
	in ReaderInput,
	sessionMode string,
	catalog *extpacks.EffectiveCatalog,
) (canSpawnImplementer, canSpawnWebResearch, canSpawnWorkers bool) {
	if len(in.SpawnAllowlist) == 0 {
		return false, false, false
	}
	var byID map[string]agentdef.Profile
	if catalog != nil {
		if defs, err := agentdef.LoadEffectiveWithCatalog(catalog); err == nil {
			byID = make(map[string]agentdef.Profile, len(defs))
			for _, d := range defs {
				byID[d.ID] = d
			}
		}
	}
	seen := make(map[string]bool, len(in.SpawnAllowlist))
	kept := 0
	for _, raw := range in.SpawnAllowlist {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if byID != nil {
			if _, exists := byID[name]; !exists {
				continue
			}
		}
		isExternal := false
		if prof, ok := byID[name]; ok {
			isExternal = slices.Contains(prof.Capabilities, agentdef.CapabilityExternal)
		} else {
			isExternal = agentdef.DeclaresAny(name, agentdef.CapabilityExternal)
		}
		if !in.WebSearch && isExternal {
			continue
		}
		mutCapable, okCap := AgentMutationCapable(name)
		if sessionMode == "ask" && okCap && mutCapable {
			continue
		}
		if in.RootCount == 0 && !AgentRunsWithoutWorkspace(name) {
			continue
		}
		kept++
		if okCap && mutCapable {
			canSpawnImplementer = true
		}
		if isExternal {
			canSpawnWebResearch = true
		}
	}
	return canSpawnImplementer, canSpawnWebResearch, canSpawnImplementer || canSpawnWebResearch || kept > 0
}

func buildReaderFacts(
	in ReaderInput,
	profile sandbox.ToolProfile,
	profileID, surfaceID, mode string,
	offered, requestable []string,
	commandOnFloor bool,
	effects toolcontract.CommandEffects,
) (map[string]any, error) {
	loadNativeToolFamilies()
	isCoordinator := surfaceID != "" || profileID == CoordinatorProfileID
	offeredSet := toolNameSet(offered)
	command := commandEffectFlags(offeredSet["command"], effects)
	reachHeld := readerProfileHeldSet(profile, offeredSet, effects, in.CommandExternalHidden)
	sessionMode := normalizeSessionMode(in.SessionMode)

	writeTools := orderedEnabledTools(nativeWriteTools, offeredSet)
	if command.mutation {
		writeTools = append([]string{"command"}, writeTools...)
	}
	surveyTools := orderedEnabledTools(surveyToolDisplayOrder, offeredSet)

	rawCanRequest := requestableToolSet(offered, requestable)
	loadableCommand := commandEffectFlags(rawCanRequest["command"], effects)

	allReachable := toolNameSet(append(slices.Clone(offered), requestable...))
	if !(command.mutation || loadableCommand.mutation) {
		delete(allReachable, "command")
	}
	mutationCapable := toolSetMutationCapable(allReachable)

	catalog, _ := extpacks.CatalogForConsumers()
	schemas := in.Schemas
	if (schemas == nil || len(schemas.Tools) == 0) && catalog != nil {
		if loadedSchemas, err := loadToolSchemaCached(catalog); err == nil && loadedSchemas != nil {
			schemas = loadedSchemas
		}
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

	connects := command.mutation && command.externalExec
	canSpawnImpl, canSpawnWeb, canSpawnWorkers := deriveSpawnFactFlags(in, sessionMode, catalog)
	hasAnyGit := in.HasGit || in.HasAnyGitRoot
	canGitRead := in.HasGit && !in.OnWorkerBranch && offeredSet["command"]
	canAnyGitRead := hasAnyGit && !in.OnWorkerBranch && offeredSet["command"]
	canGitCommit := canGitRead && command.mutation
	canGitBisect := canGitCommit && command.externalExec
	canRequestGitRead := in.HasGit && !in.OnWorkerBranch && rawCanRequest["command"]
	canRequestGitCommit := canRequestGitRead && loadableCommand.mutation
	canRequestGitBisect := canRequestGitCommit && loadableCommand.externalExec
	sharedTemp := slices.Clone(in.SharedTempLocations)

	f := ReaderFacts{
		CanAnyGitRead:                 canAnyGitRead,
		CanCommandExternalSelector:    reachHeld[toolcontract.ReachCommandExternalExecSelector],
		CanCommandVerify:              offeredSet["command"] && command.externalExec,
		CanGitBisect:                  canGitBisect,
		CanGitCommit:                  canGitCommit,
		CanGitRead:                    canGitRead,
		CanOrient:                     offeredSet["pack_board"] || offeredSet["list_dir"],
		CanRequestCommandVerify:       loadableCommand.externalExec,
		CanRequestGitBisect:           canRequestGitBisect,
		CanRequestGitCommit:           canRequestGitCommit,
		CanRequestGitRead:             canRequestGitRead,
		CanSpawnImplementer:           canSpawnImpl,
		CanSpawnWebResearch:           canSpawnWeb,
		CanSpawnWorkers:               canSpawnWorkers,
		CapsVision:                    false,
		CommandExternalHidden:         in.CommandExternalHidden,
		CommandExternalShown:          !in.CommandExternalHidden,
		CommandOnFloor:                commandOnFloor,
		CommandOutputFiles:            offeredSet["command"],
		ExecutionMode:                 mode,
		HasAnyGitRoot:                 hasAnyGit,
		HasFileTools:                  in.RootCount > 0 && hasOfferedProjectFileTool(offeredSet),
		HasGit:                        in.HasGit,
		HasWorkspaceRoots:             in.RootCount > 0,
		IsCoordinator:                 isCoordinator,
		MoreToolsLoadable:             false,
		MultiRoot:                     in.RootCount > 1,
		NativeEditLimitMiB:            readcaps.MaxMutationBytes >> 20,
		OnWorkerBranch:                in.OnWorkerBranch,
		ProfileCommandMutation:        command.mutation,
		ProfileCommandExternalExec:    command.externalExec,
		ProfileCommandNetworkEgress:   command.networkEgress,
		ProfileHasCodeRewrite:         command.mutation && (offeredSet["code_rewrite"] || toolcontract.CommandLanguageReachable("code-rewrite", reachHeld)),
		ProfileHasManagedSecrets:      len(nativeSecretTools) > 0 && allPresent(nativeSecretTools, offeredSet),
		ProfileHasPageControls:        hasToolFamily(offeredSet, "page"),
		ProfileHasRetrieval:           profileHasRetrievalTool(offeredSet),
		ProfileHasScanDrilldown:       profileHasScanDrilldownTools(offeredSet),
		ProfileHasSummarize:           offeredSet["summarize"] || toolcontract.CommandLanguageReachable("summarize", reachHeld),
		ProfileHasSurveyRepo:          offeredSet["survey_repo"] || toolcontract.CommandLanguageReachable("repo-map", reachHeld),
		ProfileHasTerminalCapture:     command.externalExec,
		ProfileHasWriteTools:          len(writeTools) > 0 || command.mutation,
		ProfileMutationCapable:        mutationCapable,
		ProgressGatedTools:            progressGatedOfferedTools(offeredSet, command.mutation),
		ProgressUngatedLifecycleTools: progress.UngatedLifecycleTools(offeredSet),
		RecallMayWiden:                isCoordinator,
		ReviewsConfinedWrites:         in.ReviewsConfinedWrites,
		RootCount:                     in.RootCount,
		RunnerCapabilityRequestTools:  projectedSchemaTools(offered, schemas, command.externalExec, "capability_request", true, command.externalExec),
		RunnerInlineWaitSeconds:       int(toolcontract.ProgramInlineWait / time.Second),
		RunnerParkSeconds:             int(toolcontract.ProgramParkThreshold / time.Second),
		RunnerDirectIPTools:           runnerCapabilityTools(offered, toolcontract.CapabilityDirectIP, connects),
		RunnerHostResourcesTools:      projectedSchemaTools(offered, schemas, command.externalExec, "capability_request.host_resources", false, command.externalExec),
		RunnerLocalListenTools:        runnerCapabilityTools(offered, toolcontract.CapabilityLocalListen, connects),
		RunnerLoopbackConnectTools:    runnerCapabilityTools(offered, toolcontract.CapabilityLoopbackConnect, connects),
		RunnerSecretUseTools:          projectedSchemaTools(offered, schemas, command.externalExec, "secret_use", false, command.externalExec),
		RunnerSocketTools:             runnerCapabilityTools(offered, toolcontract.CapabilitySocket, connects),
		RunnerSocksProxyTools:         projectedSchemaTools(offered, schemas, command.externalExec, "socks_proxy", false, command.externalExec && command.networkEgress),
		RunnerWriteRootTools:          runnerCapabilityTools(offered, toolcontract.CapabilityWriteRoot, command.mutation && command.externalExec),
		SessionMode:                   sessionMode,
		SessionScratchDir:             strings.TrimSpace(in.SessionScratchDir),
		SharedTempLocations:           sharedTemp,
		SharedTempWritable:            len(sharedTemp) > 0,
		SurfaceID:                     surfaceID,
		ToolCheckScanDrilldownTools:   scanDrilldownToolsForCheck(offeredSet),
		ToolCheckSurveyTools:          surveyTools,
		ToolCheckVerifyTools:          verifyToolsForCheck(offeredSet, command.externalExec),
		ToolCheckWriteTools:           writeTools,
		ToolProfile:                   profileID,
		WebSearchEnabled:              in.WebSearch,
	}

	if isCoordinator {
		if err := fillCoordinatorReaderFacts(in, surfaceID, offered, requestable, offeredSet, command.mutation, command.externalExec, schemas, &f); err != nil {
			return nil, err
		}
	} else {
		if err := fillWorkerReaderFacts(profile, offered, requestable, command.externalExec, toolViews, schemas, catalog, &f); err != nil {
			return nil, err
		}
	}

	if err := populateReaderFamilyMaps(in.WebSearch, offeredSet, rawCanRequest, reachHeld, catalog, &f); err != nil {
		return nil, err
	}
	return f.Map(), nil
}

func populateReaderFamilyMaps(
	webSearch bool,
	offeredSet, rawCanRequest, reachHeld map[string]bool,
	catalog *extpacks.EffectiveCatalog,
	f *ReaderFacts,
) error {
	vocab, err := BuildReaderFactVocabulary(catalog)
	if err != nil {
		return err
	}
	f.ProfileHas = make(map[string]bool, len(vocab.Tools))
	for name := range vocab.Tools {
		if _, isScalar := declaredReaderFactScalars["profile_has_"+name]; isScalar {
			continue
		}
		f.ProfileHas[name] = offeredSet[name]
	}
	for name, ok := range offeredSet {
		if ok && toolschema.ValidToolName(name) {
			if _, isScalar := declaredReaderFactScalars["profile_has_"+name]; !isScalar {
				f.ProfileHas[name] = true
			}
		}
	}
	if !webSearch {
		f.ProfileHas["fetch_url"] = false
		f.ProfileHas["web_search"] = false
	}

	f.ProfileCanRequest = make(map[string]bool, len(vocab.Tools))
	for name := range vocab.Tools {
		f.ProfileCanRequest[name] = rawCanRequest[name]
	}
	for name, ok := range rawCanRequest {
		if ok {
			f.ProfileCanRequest[name] = true
		}
	}

	f.Mints = evidenceMints(reachHeld)
	f.ReceiptKind = evidence.ActiveBinding().ReceiptKinds()
	for name := range vocab.ReceiptTools {
		if _, ok := f.ReceiptKind[name]; !ok {
			f.ReceiptKind[name] = ""
		}
	}

	f.AgentHasSkill = make(map[string]bool, len(vocab.SkillKeys))
	for _, key := range vocab.SkillKeys {
		f.AgentHasSkill[key] = false
	}
	return nil
}

func fillCoordinatorReaderFacts(
	in ReaderInput,
	surfaceID string,
	offered, requestable []string,
	offeredSet map[string]bool,
	commandMutates bool,
	commandExternalExec bool,
	schemas *toolschema.Config,
	f *ReaderFacts,
) error {
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
		return err
	}
	f.ReadGlobs = scopeData.ReadGlobs
	f.WriteGlobs = scopeData.WriteGlobs
	f.LaneSSyncTools = LaneSyncTools(offered)
	f.VisualReviewCadence = "on_request"
	f.MoreToolsLoadable = len(requestable) > 0
	if len(requestable) > 0 && offeredSet["request_tools"] {
		roster, err := RequestableTools(requestable, schemas)
		if err != nil {
			return err
		}
		f.RequestableCapabilities = RequestableCapabilities(roster)
	}
	if surfaceID != "" {
		card, err := LoadCoordinatorSurfaceCard(surfaceID)
		if err != nil {
			return err
		}
		cv := CoordinatorSurfaceCardVars(card, offered, requestable, commandMutates)
		f.SurfaceCardLabel = cv.Label
		f.SurfaceCardRule = cv.Rule
		f.SurfacePublishesText = cv.PublishesText
		f.CardMayEdit = cv.MayEdit
		f.CardMayRun = cv.MayRun
		f.CardMayDispatch = cv.MayDispatch
		f.CardEditDeferred = cv.EditDeferred
		f.CardOnlyTools = cv.OnlyTools
	}
	runOffered := offered
	if !commandExternalExec {
		runOffered = filterToolName(offered, "command")
	}
	fillVisualShowFacts(runOffered, requestable, f)
	return nil
}

func fillWorkerReaderFacts(
	profile sandbox.ToolProfile,
	offered, requestable []string,
	commandExternalExec bool,
	toolViews []AgentToolArgView,
	schemas *toolschema.Config,
	catalog *extpacks.EffectiveCatalog,
	f *ReaderFacts,
) error {
	toolsCtx := make([]map[string]any, len(toolViews))
	for i, t := range toolViews {
		toolsCtx[i] = map[string]any{
			"name":          t.Name,
			"required_args": t.RequiredArgs,
		}
	}
	f.AgentTools = toolsCtx
	f.WriteGlobs = slices.Clone(profile.WriteGlobs)
	f.MoreToolsLoadable = len(requestable) > 0
	if len(requestable) > 0 {
		roster, err := RequestableTools(requestable, schemas)
		if err != nil {
			return err
		}
		f.RequestableCapabilities = RequestableCapabilities(roster)
	}
	if catalog != nil {
		if policy, err := loadHintPolicyCached(catalog); err == nil {
			if hints, err := loadHintCodeRowsCached(catalog.Revision, policy); err == nil {
				rejectCodes := recoverableRejectCodes(hints, append(slices.Clone(offered), requestable...))
				codesCtx := make([]map[string]any, len(rejectCodes))
				for i, c := range rejectCodes {
					codesCtx[i] = map[string]any{
						"code":               c.Code,
						"branch_instruction": c.BranchInstruction,
					}
				}
				f.RejectCodes = codesCtx
			}
		}
	}
	runOffered := offered
	if !commandExternalExec {
		runOffered = filterToolName(offered, "command")
	}
	fillVisualShowFacts(runOffered, requestable, f)
	return nil
}
