package prompts

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
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
	reachHeld := readerHeldSet(offeredSet, effects)

	writeTools := orderedEnabledTools(nativeWriteTools, offeredSet)
	if command.mutation {
		writeTools = append([]string{"command"}, writeTools...)
	}
	surveyTools := orderedEnabledTools(surveyToolDisplayOrder, offeredSet)

	allReachable := toolNameSet(append(slices.Clone(offered), requestable...))
	if !command.mutation {
		delete(allReachable, "command")
	}
	mutationCapable := toolSetMutationCapable(allReachable)

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

	connects := command.mutation && (command.externalExec || command.networkEgress)

	f := ReaderFacts{
		CapsVision:                    false,
		CommandOnFloor:                commandOnFloor,
		CommandOutputFiles:            offeredSet["command"],
		ExecutionMode:                 mode,
		HasGit:                        in.HasGit,
		IsCoordinator:                 isCoordinator,
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
		RootCount:                     in.RootCount,
		RunnerDirectIPTools:           runnerCapabilityTools(offered, toolcontract.CapabilityDirectIP, connects),
		RunnerLocalListenTools:        runnerCapabilityTools(offered, toolcontract.CapabilityLocalListen, connects),
		RunnerLoopbackConnectTools:    runnerCapabilityTools(offered, toolcontract.CapabilityLoopbackConnect, connects),
		RunnerSocketTools:             runnerCapabilityTools(offered, toolcontract.CapabilitySocket, connects),
		RunnerWriteRootTools:          runnerCapabilityTools(offered, toolcontract.CapabilityWriteRoot, command.mutation),
		SurfaceID:                     surfaceID,
		ToolCheckScanDrilldownTools:   scanDrilldownToolsForCheck(offeredSet),
		ToolCheckSurveyTools:          surveyTools,
		ToolCheckVerifyTools:          verifyToolsForCheck(offeredSet, command.externalExec),
		ToolCheckWriteTools:           writeTools,
		ToolProfile:                   profileID,
		WebSearchEnabled:              in.WebSearch,
	}

	if isCoordinator {
		if err := fillCoordinatorReaderFacts(in, surfaceID, offered, requestable, offeredSet, command.mutation, schemas, &f); err != nil {
			return nil, err
		}
	} else {
		if err := fillWorkerReaderFacts(profile, offered, requestable, toolViews, schemas, catalog, &f); err != nil {
			return nil, err
		}
	}

	vocab, err := BuildReaderFactVocabulary(catalog)
	if err != nil {
		return nil, err
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
	if !in.WebSearch {
		f.ProfileHas["fetch_url"] = false
		f.ProfileHas["web_search"] = false
	}

	rawCanRequest := requestableToolSet(offered, requestable)
	f.ProfileCanRequest = make(map[string]bool, len(vocab.Tools))
	for name := range vocab.Tools {
		f.ProfileCanRequest[name] = rawCanRequest[name]
	}
	for name, ok := range rawCanRequest {
		if ok {
			f.ProfileCanRequest[name] = true
		}
	}

	f.Mints = evidenceMintsForCommand(offeredSet, command.mutation, command.externalExec)
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

	return f.Map(), nil
}

func fillCoordinatorReaderFacts(
	in ReaderInput,
	surfaceID string,
	offered, requestable []string,
	offeredSet map[string]bool,
	commandMutates bool,
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
	if !commandMutates {
		runOffered = filterToolName(offered, "command")
	}
	fillVisualShowFacts(runOffered, requestable, f)
	return nil
}

func fillWorkerReaderFacts(
	profile sandbox.ToolProfile,
	offered, requestable []string,
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
	fillVisualShowFacts(offered, requestable, f)
	return nil
}
