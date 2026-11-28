package hoststate

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/readerfacts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

// TurnAuthority freezes the exact production-projected tool schemas, native
// program reach, and prompt Reader for one coordinator or worker turn.
type TurnAuthority struct {
	Metas           []tools.ToolMeta
	Schemas         map[string]map[string]any
	PostLoadSchemas map[string]map[string]any
	Offered         []string
	Plan            toolsurface.Plan
	ProgramReach    toolreach.Programs
	ProgramTable    *toolreach.ProgramTable
	Reach           toolreach.Request
	Reader          prompts.Reader
	Digest          string
}

// ActorAuthority adapts TurnAuthority for toolvocabtest.EvaluateClaimBlock.
func (a TurnAuthority) ActorAuthority() toolvocabtest.ActorAuthority {
	return toolvocabtest.AuthorityFromReaderAndMetas(
		a.Reader,
		a.ProgramReach,
		a.Metas,
		toolvocabtest.WithProgramTable(a.ProgramTable),
		toolvocabtest.WithPostLoadAuthority(a.PostLoadSchemas, a.ProgramTable),
	)
}

type executorCacheKey struct {
	revision       string
	posture        api.SessionPosture
	askClamped     bool
	webSearch      bool
	externalHidden bool
	mcpState       MCPState
}

type programTableKey struct {
	profileID string
	surfaceID string
	rootCount int
}

type executorBundle struct {
	exec     *toolexecution.Executor
	reg      *tools.DefaultRegistry
	tablesMu *sync.Mutex
	tables   map[programTableKey]*toolreach.ProgramTable
	reaches  map[programTableKey]toolreach.Programs
}

type projectedTurnToolsEntry struct {
	metas            []tools.ToolMeta
	plan             toolsurface.Plan
	resolvedSurface  string
	progReach        toolreach.Programs
	progTable        *toolreach.ProgramTable
	postLoadSchemas  map[string]map[string]any
	offered          []string
	projectedSchemas map[string]map[string]any
	schemasDigest    string
	schemasOK        bool
	programs         []string
}

var (
	executorCacheMu   sync.Mutex
	executorCache     = map[executorCacheKey]executorBundle{}
	cachedSandboxConf = sync.OnceValues(sandbox.LoadConfig)
	projTurnMu        sync.Mutex
	projTurnCache     = map[string]projectedTurnToolsEntry{}
	frozenTurnMu      sync.RWMutex
	frozenTurnCache   = map[string]TurnAuthority{}
	frozenWorkerMu    sync.RWMutex
	frozenWorkerCache = map[string]TurnAuthority{}

	authorityMemoMu    sync.Mutex
	authorityMemoHolds int
)

// HoldAuthorityMemos keeps the executor, registry and turn-projection memos
// that freezes share until the returned release runs; the last open hold's
// release drops them. Freezes outside any hold still fill the memos.
func HoldAuthorityMemos() (release func()) {
	authorityMemoMu.Lock()
	authorityMemoHolds++
	authorityMemoMu.Unlock()
	return sync.OnceFunc(func() {
		authorityMemoMu.Lock()
		defer authorityMemoMu.Unlock()
		authorityMemoHolds--
		if authorityMemoHolds > 0 {
			return
		}
		executorCacheMu.Lock()
		executorCache = map[executorCacheKey]executorBundle{}
		executorCacheMu.Unlock()
		registryCacheMu.Lock()
		registryCache = map[registryCacheKey]*tools.DefaultRegistry{}
		registryCacheMu.Unlock()
		projTurnMu.Lock()
		projTurnCache = map[string]projectedTurnToolsEntry{}
		projTurnMu.Unlock()
		frozenTurnMu.Lock()
		frozenTurnCache = map[string]TurnAuthority{}
		frozenTurnMu.Unlock()
		frozenWorkerMu.Lock()
		frozenWorkerCache = map[string]TurnAuthority{}
		frozenWorkerMu.Unlock()
	})
}

func sortedBoolKeys(m map[string]bool) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

// NarrowProfilesForPosture narrows every profile's CommandEffects by posture,
// and optionally clamps to read_only for Ask-spawned child workers.
func NarrowProfilesForPosture(
	profiles []sandbox.ToolProfile,
	posture api.SessionPosture,
	clampReadOnly bool,
) ([]sandbox.ToolProfile, error) {
	postures, err := Postures(nil)
	if err != nil {
		return nil, err
	}
	var allowed toolcontract.CommandEffects
	found := false
	for _, p := range postures {
		if p.ID == posture {
			allowed = p.CommandEffects
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown session posture %q", posture)
	}
	if clampReadOnly {
		ro, roErr := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
		if roErr != nil {
			return nil, roErr
		}
		allowed &= ro
	}
	out := slices.Clone(profiles)
	for i := range out {
		out[i].CommandEffects &= allowed
	}
	return out, nil
}

// ExecutorForOverlay constructs or reuses a production-configured Executor
// backed by the catalog's tool schemas and posture-narrowed sandbox profiles.
func ExecutorForOverlay(
	overlay PackOverlay,
	posture api.SessionPosture,
	clampReadOnly, webSearch, externalHidden bool,
	mcpState MCPState,
) (*toolexecution.Executor, []sandbox.ToolProfile, error) {
	bundle, narrowed, err := executorBundleForOverlay(overlay, posture, clampReadOnly, webSearch, externalHidden, mcpState)
	if err != nil {
		return nil, nil, err
	}
	return bundle.exec, narrowed, nil
}

// RegistryAndExecutorForOverlay returns both the catalog ToolRegistry and Executor for overlay.
func RegistryAndExecutorForOverlay(
	overlay PackOverlay,
	posture api.SessionPosture,
	clampReadOnly, webSearch, externalHidden bool,
	mcpState MCPState,
) (*tools.DefaultRegistry, *toolexecution.Executor, []sandbox.ToolProfile, error) {
	bundle, narrowed, err := executorBundleForOverlay(overlay, posture, clampReadOnly, webSearch, externalHidden, mcpState)
	if err != nil {
		return nil, nil, nil, err
	}
	return bundle.reg, bundle.exec, narrowed, nil
}

func executorBundleForOverlay(
	overlay PackOverlay,
	posture api.SessionPosture,
	clampReadOnly, webSearch, externalHidden bool,
	mcpState MCPState,
) (executorBundle, []sandbox.ToolProfile, error) {
	narrowed, err := NarrowProfilesForPosture(overlay.Profiles, posture, clampReadOnly)
	if err != nil {
		return executorBundle{}, nil, err
	}
	key := executorCacheKey{
		revision:       overlay.Catalog.Revision,
		posture:        posture,
		askClamped:     clampReadOnly,
		webSearch:      webSearch,
		externalHidden: externalHidden,
		mcpState:       mcpState,
	}
	executorCacheMu.Lock()
	if cached, ok := executorCache[key]; ok {
		executorCacheMu.Unlock()
		return cached, narrowed, nil
	}
	executorCacheMu.Unlock()

	reg, err := catalogRegistry(overlay.Catalog, mcpState)
	if err != nil {
		return executorBundle{}, nil, err
	}
	sandboxCfg, err := cachedSandboxConf()
	if err != nil {
		return executorBundle{}, nil, fmt.Errorf("load sandbox config: %w", err)
	}
	boundary := sandbox.NewBoundary(sandboxCfg, narrowed)
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	if !webSearch {
		policy.SetRuntimeToolDeny(func(toolName string) bool {
			return toolName == webresearch.SearchToolName || toolName == webresearch.FetchURLToolName
		})
	}
	exec := toolexecution.NewExecutor(policy, reg, toolprofiles.DefaultToolProfileID)
	exec.Metadata.SetToolSchemas(overlay.Schemas)
	exec.Metadata.SetCommandExternalHidden(externalHidden)

	bundle := executorBundle{
		exec:     exec,
		reg:      reg,
		tablesMu: &sync.Mutex{},
		tables:   make(map[programTableKey]*toolreach.ProgramTable),
		reaches:  make(map[programTableKey]toolreach.Programs),
	}
	executorCacheMu.Lock()
	executorCache[key] = bundle
	executorCacheMu.Unlock()
	return bundle, narrowed, nil
}

func (b executorBundle) programReach(ctx context.Context, filter platform.ToolFilter) (toolreach.Programs, error) {
	key := programTableKey{profileID: filter.ProfileID, surfaceID: filter.SurfaceID, rootCount: filter.ProjectRootCount}
	b.tablesMu.Lock()
	if cached, ok := b.reaches[key]; ok {
		b.tablesMu.Unlock()
		return cached, nil
	}
	b.tablesMu.Unlock()
	reach, err := b.exec.Metadata.ProgramReach(ctx, filter)
	if err != nil {
		return nil, err
	}
	b.tablesMu.Lock()
	b.reaches[key] = reach
	b.tablesMu.Unlock()
	return reach, nil
}

func (b executorBundle) programTable(ctx context.Context, filter platform.ToolFilter) (*toolreach.ProgramTable, error) {
	key := programTableKey{profileID: filter.ProfileID, surfaceID: filter.SurfaceID, rootCount: filter.ProjectRootCount}
	b.tablesMu.Lock()
	if cached, ok := b.tables[key]; ok {
		b.tablesMu.Unlock()
		return cached, nil
	}
	b.tablesMu.Unlock()
	table, err := b.exec.Metadata.ProgramTable(ctx, filter)
	if err != nil {
		return nil, err
	}
	b.tablesMu.Lock()
	b.tables[key] = table
	b.tablesMu.Unlock()
	return table, nil
}

type registryCacheKey struct {
	revision string
	mcpState MCPState
}

var (
	registryCacheMu sync.Mutex
	registryCache   = map[registryCacheKey]*tools.DefaultRegistry{}
)

// catalogRegistry shares one registry per catalog and MCP fixture: posture,
// web search and external hiding narrow the executor's policy, not the
// registry, and executors only read it.
func catalogRegistry(eff *extpacks.EffectiveCatalog, mcpState MCPState) (*tools.DefaultRegistry, error) {
	key := registryCacheKey{revision: eff.Revision, mcpState: mcpState}
	registryCacheMu.Lock()
	defer registryCacheMu.Unlock()
	if reg, ok := registryCache[key]; ok {
		return reg, nil
	}
	reg, err := buildCatalogRegistry(eff, mcpState)
	if err != nil {
		return nil, err
	}
	registryCache[key] = reg
	return reg, nil
}

func buildCatalogRegistry(eff *extpacks.EffectiveCatalog, mcpState MCPState) (*tools.DefaultRegistry, error) {
	regSchemas, err := extpacks.LoadEffectiveRegistrySchemas(eff)
	if err != nil {
		return nil, fmt.Errorf("load registry schemas: %w", err)
	}
	reg, err := tools.NewCatalogRegistry(regSchemas)
	if err != nil {
		return nil, fmt.Errorf("new catalog registry: %w", err)
	}
	noop := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
	for name := range regSchemas.Tools {
		if toolcontract.IsCommandOperation(name) {
			_ = reg.RegisterOperation(name)
			continue
		}
		if _, hasContract := toolcontract.Lookup(name); hasContract {
			if regErr := reg.Register(name, noop); regErr != nil {
				return nil, regErr
			}
		}
	}
	if err := registerFixtureMCPTool(reg, mcpState); err != nil {
		return nil, err
	}
	return reg, nil
}

func registerFixtureMCPTool(reg *tools.DefaultRegistry, mcpState MCPState) error {
	if mcpState == MCPNone {
		return nil
	}
	readOnly := mcpState == MCPReadOnlyHint
	contract, ok := toolcontract.Lookup("mcp_fixture_inspect")
	if !ok {
		contract = toolcontract.Contract{Owner: "mcp"}
	}
	return reg.RegisterDefinition(tools.Definition{
		Meta: tools.ToolMeta{
			Name:         "mcp_fixture_inspect",
			Description:  "Inspect external MCP fixture state.",
			Source:       tools.ToolSourceMCP,
			SourceID:     "fixture",
			AlwaysLoad:   true,
			ReadOnlyHint: readOnly,
			ArgsSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
			},
		},
		Contract: contract,
		Handler:  func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil },
	})
}

// FreezeTurnAuthority computes the production tool list, schema projections,
// native program reach, and Reader for a CoordinatorState through the live
// toolpolicy.Engine and promptloop.ProjectTurnTools pipeline.
func FreezeTurnAuthority(ctx context.Context, state CoordinatorState) (TurnAuthority, error) {
	return FreezeTurnAuthorityWithResources(ctx, state, toolcontract.ResourcePresence{})
}

// FreezeTurnAuthorityWithResources computes the production tool list, schema
// projections, native program reach, and Reader for state under explicit live
// resource presence (active background jobs or held calls).
func FreezeTurnAuthorityWithResources(
	ctx context.Context,
	state CoordinatorState,
	resources toolcontract.ResourcePresence,
) (TurnAuthority, error) {
	overlay := state.Overlay
	if overlay.Catalog == nil {
		overlays, err := DiscoverPackOverlays(ctx)
		if err != nil {
			return TurnAuthority{}, err
		}
		overlay = overlays[0]
	}
	bundle, narrowed, err := executorBundleForOverlay(
		overlay, state.Posture, false, state.WebSearch, state.CommandExternalHidden, state.MCPState,
	)
	if err != nil {
		return TurnAuthority{}, err
	}

	var proj projectedTurnToolsEntry
	cacheKey := ""
	fullKey := ""
	if len(state.History) == 0 {
		if state.TurnProfile.SurfaceID != "" {
			// ProjectTurnTools depends only on resolved SurfaceID, active-run presence
			// (and PhaseCoordinatorSurface under Ask), root count, posture/mode/overlay,
			// spawn allowlist, loaded tools, and live resource presence.
			hasRun := state.RunContext.RunID != "" || state.TurnProfile.WorkflowActive
			askPhaseSurf := ""
			if state.SessionMode == api.SessionModeAsk && hasRun {
				askPhaseSurf = state.RunContext.PhaseCoordinatorSurface
				if askPhaseSurf == "" {
					askPhaseSurf = state.TurnProfile.PhaseCoordinatorSurface
				}
				if askPhaseSurf == "" {
					askPhaseSurf = state.TurnProfile.SurfaceID
				}
			}
			cacheKey = fmt.Sprintf(
				"cs|%s|%s|%s|%t|%t|%s|%d|%s|%t|%s|%s|%s|jobs=%t|held=%t",
				overlay.Catalog.Revision, state.SessionMode, state.Posture,
				state.WebSearch, state.CommandExternalHidden, state.MCPState, state.RootCount,
				state.TurnProfile.SurfaceID, hasRun, askPhaseSurf,
				strings.Join(state.SpawnAllowlist, ","), sortedBoolKeys(state.Loaded),
				resources.CommandJobs, resources.HeldCalls,
			)
		} else {
			cacheKey = fmt.Sprintf(
				"c|%s|%s|%s|%t|%t|%s|%d|%s|%t|%s|%s|%s|%s|%s|%t|%t|%s|%+v|%s|jobs=%t|held=%t",
				overlay.Catalog.Revision, state.SessionMode, state.Posture,
				state.WebSearch, state.CommandExternalHidden, state.MCPState, state.RootCount,
				state.TurnProfile.SurfaceID, state.TurnProfile.WorkflowActive,
				state.TurnProfile.AdvanceWhenGateMet, state.TurnProfile.PhaseCoordinatorSurface,
				state.RunContext.RunID, state.RunContext.WorkflowID, state.RunContext.PhaseCoordinatorSurface,
				state.RunContext.PhaseHostHeld, state.RunContext.HasComposeDraft,
				strings.Join(state.SpawnAllowlist, ","), state.ImplementState, sortedBoolKeys(state.Loaded),
				resources.CommandJobs, resources.HeldCalls,
			)
		}
		fullKey = fmt.Sprintf(
			"%s|git=%t/%t|appr=%s|scratch=%s|temp=%+v|vision=%t|allowed=%s|roots=%+v",
			cacheKey, state.HasGit, state.HasAnyGitRoot, state.ApprovalPosture,
			state.SessionScratchDir, state.SharedTempLocations, state.Vision,
			strings.Join(state.RunContext.AllowedAgents, ","), state.Roots,
		)
		frozenTurnMu.RLock()
		if cachedAuth, ok := frozenTurnCache[fullKey]; ok {
			frozenTurnMu.RUnlock()
			return cachedAuth, nil
		}
		frozenTurnMu.RUnlock()
		projTurnMu.Lock()
		cached, hit := projTurnCache[cacheKey]
		projTurnMu.Unlock()
		if hit {
			proj = cached
		}
	}
	if proj.offered == nil {
		deps, sess, history, frame := buildCoordinatorTurnInputs(state, bundle, resources)
		turn, pErr := promptloop.ProjectTurnTools(ctx, deps, sess, history, frame)
		if pErr != nil {
			return TurnAuthority{}, fmt.Errorf("ProjectTurnTools: %w", pErr)
		}
		metas, plan, resolvedSurfaceID := turn.Metas, turn.Plan, turn.SurfaceID
		if state.TurnProfile.SurfaceID != "" && resolvedSurfaceID != state.TurnProfile.SurfaceID {
			return TurnAuthority{}, fmt.Errorf(
				"resolved surface %q disagrees with state.TurnProfile.SurfaceID %q",
				resolvedSurfaceID, state.TurnProfile.SurfaceID,
			)
		}
		filter := platform.ToolFilter{
			ProfileID:        prompts.CoordinatorProfileID,
			SurfaceID:        resolvedSurfaceID,
			ProjectRootCount: state.RootCount,
		}
		progReach, rErr := bundle.programReach(ctx, filter)
		if rErr != nil {
			return TurnAuthority{}, fmt.Errorf("Metadata.ProgramReach: %w", rErr)
		}
		progTable, tErr := bundle.programTable(ctx, filter)
		if tErr != nil {
			return TurnAuthority{}, fmt.Errorf("Metadata.ProgramTable: %w", tErr)
		}
		postLoadSchemas := turn.DeferredSchemas
		offered := toolMetaNames(metas)
		projectedSchemas := toolMetaSchemas(metas)
		schemasDigest, encErr := digestAdmittedValue(projectedSchemas)
		proj = projectedTurnToolsEntry{
			metas:            metas,
			plan:             plan,
			resolvedSurface:  resolvedSurfaceID,
			progReach:        progReach,
			progTable:        progTable,
			postLoadSchemas:  postLoadSchemas,
			offered:          offered,
			projectedSchemas: projectedSchemas,
			schemasDigest:    schemasDigest,
			schemasOK:        encErr == nil,
			programs:         computeProgramsList(progReach),
		}
		if cacheKey != "" {
			projTurnMu.Lock()
			projTurnCache[cacheKey] = proj
			projTurnMu.Unlock()
		}
	}

	host, err := CoordinatorHostFacts(state, narrowed, overlay.Schemas)
	if err != nil {
		return TurnAuthority{}, err
	}
	reader, err := prompts.NewReader(host, prompts.TurnProjection{
		ProfileID:        prompts.CoordinatorProfileID,
		SurfaceID:        proj.resolvedSurface,
		Plan:             proj.plan,
		Offered:          proj.offered,
		ProjectedSchemas: mergedSchemas(proj.postLoadSchemas, proj.projectedSchemas),
	})
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("prompts.NewReader: %w", err)
	}
	reader = reader.WithVision(state.Vision)

	assembled := assembleTurnAuthorityFromEntry(proj, reader)
	if fullKey != "" {
		frozenTurnMu.Lock()
		frozenTurnCache[fullKey] = assembled
		frozenTurnMu.Unlock()
	}
	return assembled, nil
}

func buildCoordinatorTurnInputs(
	state CoordinatorState,
	bundle executorBundle,
	resources toolcontract.ResourcePresence,
) (promptloop.ContextDeps, *api.Session, []api.Message, inject.CoordinatorTurnFrame) {
	sess := coordinatorFixtureSession(state)

	history := state.History
	if len(history) == 0 {
		history = []api.Message{{Role: api.MessageRoleUser, Content: "Inspect current state"}}
	}

	runCtx := NormalizeCoordinatorRunContext(state, sess, history)
	var roster *inject.AgentRoster
	if state.SpawnAllowlist != nil {
		roster = &inject.AgentRoster{Effective: slices.Clone(state.SpawnAllowlist)}
	}
	frame := inject.CoordinatorTurnFrame{
		RunContext: runCtx,
		Roster:     roster,
		Machine:    inject.Machine{ProfileID: prompts.CoordinatorProfileID, SkillCount: 1},
	}

	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       bundle.exec.Metadata,
		ProjectRootCount: func(context.Context, *api.Session) int { return state.RootCount },
	})
	deps := promptloop.ContextDeps{
		Tools:  bundle.reg,
		Policy: policy,
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return state.ImplementState
		},
		LoadedTools:      func(string) map[string]bool { return maps.Clone(state.Loaded) },
		LiveResources:    func(string) toolcontract.ResourcePresence { return resources },
		ProjectRootCount: func(context.Context, *api.Session) int { return state.RootCount },
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
			WebSearchEnabled: func() bool { return state.WebSearch },
		},
	}
	return deps, sess, history, frame
}

func coordinatorFixtureSession(state CoordinatorState) *api.Session {
	sess := &api.Session{
		ID:        "coord-fixture",
		Mode:      state.SessionMode,
		Posture:   state.Posture,
		AgentType: prompts.CoordinatorProfileID,
	}
	if len(state.Roots) > 0 {
		sess.WorkspacePath = state.Roots[0].Ref.Path
	}
	return sess
}

// NormalizeCoordinatorRunContext synthesizes the minimal run-context fields needed
// so surface.ResolveTurnProfile resolves to state.TurnProfile.SurfaceID.
func NormalizeCoordinatorRunContext(
	state CoordinatorState,
	sess *api.Session,
	history []api.Message,
) api.CoordinatorRunContext {
	runCtx := state.RunContext
	if state.TurnProfile.AdvanceWhenGateMet != "" && runCtx.AdvanceWhenGateMet == "" {
		runCtx.AdvanceWhenGateMet = state.TurnProfile.AdvanceWhenGateMet
	}
	if runCtx.RunID == "" && state.TurnProfile.WorkflowActive {
		runCtx.RunID = "fixture-run"
		runCtx.WorkflowID = "fixture-workflow"
		if runCtx.PhaseCoordinatorSurface == "" {
			runCtx.PhaseCoordinatorSurface = state.TurnProfile.PhaseCoordinatorSurface
		}
		if runCtx.PhaseCoordinatorSurface == "" {
			runCtx.PhaseCoordinatorSurface = state.TurnProfile.SurfaceID
		}
	}
	if runCtx.RunID == "" && state.SessionMode != api.SessionModeAsk && state.TurnProfile.SurfaceID != "" {
		resolved := surface.ResolveTurnProfile(runCtx, sess, history, state.ImplementState)
		if resolved.SurfaceID != state.TurnProfile.SurfaceID {
			runCtx.RunID = "fixture-run"
			runCtx.WorkflowID = "fixture-workflow"
			runCtx.PhaseCoordinatorSurface = state.TurnProfile.SurfaceID
			runCtx.PhaseHostHeld = state.TurnProfile.SurfaceID == "await_host"
			runCtx.HasComposeDraft = state.TurnProfile.SurfaceID == "workflow_compose"
		}
	}
	return runCtx
}

func toolMetaNames(metas []tools.ToolMeta) []string {
	out := make([]string, len(metas))
	for i, m := range metas {
		out[i] = m.Name
	}
	slices.Sort(out)
	return out
}

func toolMetaSchemas(metas []tools.ToolMeta) map[string]map[string]any {
	out := make(map[string]map[string]any, len(metas))
	for _, m := range metas {
		out[m.Name] = m.ArgsSchema
	}
	return out
}

// CoordinatorHostFacts derives state's host facts through the production
// derivation from the observation the generated state describes.
func CoordinatorHostFacts(state CoordinatorState, profiles []sandbox.ToolProfile, schemas *toolschema.Config) (prompts.HostFacts, error) {
	sess := coordinatorFixtureSession(state)
	roots := state.Roots
	if len(roots) == 0 && state.RootCount > 0 {
		roots = synthesizedRoots(state.RootCount, state.HasGit, state.HasAnyGitRoot)
	}
	host, err := readerfacts.Derive(readerfacts.Observation{
		Session:               sess,
		Profiles:              profiles,
		Schemas:               schemas,
		Roots:                 rootRefs(roots),
		WebSearch:             state.WebSearch,
		ApprovalPosture:       state.ApprovalPosture,
		CommandExternalHidden: state.CommandExternalHidden,
		SessionScratchDir:     state.SessionScratchDir,
		SharedTempLocations:   state.SharedTempLocations,
		AllowedAgents:         state.RunContext.AllowedAgents,
		IsRepo:                rootRepoProbe(roots),
	})
	if err != nil {
		return prompts.HostFacts{}, err
	}
	if host.RootCount != state.RootCount || host.HasGit != state.HasGit || host.HasAnyGitRoot != (state.HasGit || state.HasAnyGitRoot) {
		return prompts.HostFacts{}, fmt.Errorf("state %s: derived roots=%d git=%t/%t disagree with generated roots=%d git=%t/%t",
			state.ID, host.RootCount, host.HasGit, host.HasAnyGitRoot, state.RootCount, state.HasGit, state.HasAnyGitRoot)
	}
	return host, nil
}

// synthesizedRoots describes a state that names only its root count and Git
// facts: the primary root carries the primary Git fact, and a second root any
// other Git root.
func synthesizedRoots(count int, primaryGit, anyGit bool) []RootSpec {
	out := make([]RootSpec, 0, count)
	for i := range count {
		ref := projectroot.RootRef{ID: fmt.Sprintf("r%d", i+1), Label: "root", Path: fmt.Sprintf("/tmp/fixture-root-%d", i+1), IsPrimary: i == 0}
		git := primaryGit
		if i > 0 {
			git = anyGit && !primaryGit && i == 1
		}
		out = append(out, RootSpec{Ref: ref, HasGit: git})
	}
	return out
}

func rootRefs(roots []RootSpec) []projectroot.RootRef {
	out := make([]projectroot.RootRef, 0, len(roots))
	for _, root := range roots {
		out = append(out, root.Ref)
	}
	return out
}

// rootRepoProbe answers the repository probe from the generated roots.
func rootRepoProbe(roots []RootSpec) func(string) bool {
	repos := make(map[string]bool, len(roots))
	for _, root := range roots {
		repos[root.Ref.Path] = root.HasGit
	}
	return func(path string) bool { return repos[path] }
}

func mergedSchemas(layers ...map[string]map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, layer := range layers {
		maps.Copy(out, layer)
	}
	return out
}

func assembleTurnAuthorityFromEntry(proj projectedTurnToolsEntry, reader prompts.Reader) TurnAuthority {
	mergedReachSchemas := make(map[string]map[string]any, len(proj.postLoadSchemas)+len(proj.projectedSchemas))
	maps.Copy(mergedReachSchemas, proj.postLoadSchemas)
	maps.Copy(mergedReachSchemas, proj.projectedSchemas)
	effectivePlan := toolsurface.Compile(proj.offered, reader.Requestable())
	reach := toolreach.Freeze(proj.offered, effectivePlan).WithPrograms(proj.progReach).WithSchemas(mergedReachSchemas)
	if proj.progTable != nil {
		reach = toolreach.Freeze(proj.offered, effectivePlan).WithProgramTable(proj.progTable).WithSchemas(mergedReachSchemas)
	}
	digest := ""
	if proj.schemasOK {
		digest = authorityDigestPrecomputed(proj.offered, proj.schemasDigest, proj.programs, reader.Mints(), reader)
	}
	return TurnAuthority{
		Metas:           proj.metas,
		Schemas:         proj.projectedSchemas,
		PostLoadSchemas: proj.postLoadSchemas,
		Offered:         proj.offered,
		Plan:            proj.plan,
		ProgramReach:    proj.progReach,
		ProgramTable:    proj.progTable,
		Reach:           reach,
		Reader:          reader,
		Digest:          digest,
	}
}
