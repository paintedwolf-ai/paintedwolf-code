package hoststate

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

// TurnAuthority freezes the exact production-projected tool schemas, native
// program reach, and prompt Reader for one coordinator or worker turn.
type TurnAuthority struct {
	Metas        []tools.ToolMeta
	Schemas      map[string]map[string]any
	Offered      []string
	Plan         toolsurface.Plan
	ProgramReach toolreach.Programs
	Reach        toolreach.Request
	Reader       prompts.Reader
	Digest       string
}

type executorCacheKey struct {
	revision       string
	posture        api.SessionPosture
	askClamped     bool
	webSearch      bool
	externalHidden bool
	mcpState       MCPState
}

type executorBundle struct {
	exec *toolexecution.Executor
	reg  *tools.DefaultRegistry
}

var (
	executorCacheMu sync.Mutex
	executorCache   = map[executorCacheKey]executorBundle{}
)

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

	reg, err := buildCatalogRegistry(overlay.Catalog, mcpState)
	if err != nil {
		return executorBundle{}, nil, err
	}
	sandboxCfg, err := sandbox.LoadConfig()
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

	bundle := executorBundle{exec: exec, reg: reg}
	executorCacheMu.Lock()
	executorCache[key] = bundle
	executorCacheMu.Unlock()
	return bundle, narrowed, nil
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

	deps, sess, history, frame := buildCoordinatorTurnInputs(state, bundle)
	metas, plan, resolvedSurfaceID, err := promptloop.ProjectTurnTools(ctx, deps, sess, history, frame)
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("ProjectTurnTools: %w", err)
	}
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
	progReach, err := bundle.exec.Metadata.ProgramReach(ctx, filter)
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("Metadata.ProgramReach: %w", err)
	}

	offered := toolMetaNames(metas)
	var reviewsConfinedWrites bool
	if state.ApprovalPosture != "" {
		reviewsConfinedWrites = state.ApprovalPosture.ReviewsConfinedWrites()
	}
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             prompts.CoordinatorProfileID,
		SurfaceID:             resolvedSurfaceID,
		Mode:                  surface.ExecutionModeFamily(resolvedSurfaceID),
		SessionMode:           string(state.SessionMode),
		Profiles:              narrowed,
		Plan:                  plan,
		Loaded:                state.Loaded,
		Visible:               offered,
		Schemas:               overlay.Schemas,
		RootCount:             state.RootCount,
		HasGit:                state.HasGit,
		HasAnyGitRoot:         state.HasAnyGitRoot,
		WebSearch:             state.WebSearch,
		SpawnAllowlist:        state.SpawnAllowlist,
		ReviewsConfinedWrites: reviewsConfinedWrites,
		CommandExternalHidden: state.CommandExternalHidden,
		SessionScratchDir:     state.SessionScratchDir,
		SharedTempLocations:   state.SharedTempLocations,
	})
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("prompts.NewReader: %w", err)
	}
	reader = reader.WithVision(state.Vision)

	return assembleTurnAuthority(metas, offered, plan, progReach, reader), nil
}

func buildCoordinatorTurnInputs(
	state CoordinatorState,
	bundle executorBundle,
) (promptloop.ContextDeps, *api.Session, []api.Message, inject.CoordinatorTurnFrame) {
	sess := &api.Session{
		ID:        "coord-fixture",
		Mode:      state.SessionMode,
		Posture:   state.Posture,
		AgentType: prompts.CoordinatorProfileID,
	}
	if len(state.Roots) > 0 {
		sess.WorkspacePath = state.Roots[0].Ref.Path
	} else if state.RootCount > 0 {
		sess.WorkspacePath = "/tmp/fixture-primary"
	}

	history := state.History
	if len(history) == 0 {
		history = []api.Message{{Role: api.MessageRoleUser, Content: "Inspect current state"}}
	}

	runCtx := normalizeCoordinatorRunContext(state, sess, history)
	var roster *inject.AgentRoster
	if state.SpawnAllowlist != nil {
		roster = &inject.AgentRoster{Effective: slices.Clone(state.SpawnAllowlist)}
	}
	frame := inject.CoordinatorTurnFrame{
		RunContext: runCtx,
		Roster:     roster,
		Machine:    inject.Machine{ProfileID: prompts.CoordinatorProfileID},
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
		ProjectRootCount: func(context.Context, *api.Session) int { return state.RootCount },
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
			WebSearchEnabled: func() bool { return state.WebSearch },
		},
	}
	return deps, sess, history, frame
}

func normalizeCoordinatorRunContext(
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

func assembleTurnAuthority(
	metas []tools.ToolMeta,
	offered []string,
	plan toolsurface.Plan,
	progReach toolreach.Programs,
	reader prompts.Reader,
) TurnAuthority {
	schemas := make(map[string]map[string]any, len(metas))
	for _, m := range metas {
		schemas[m.Name] = m.ArgsSchema
	}
	reach := toolreach.Freeze(offered, plan).WithPrograms(progReach)
	digest := AuthorityDigest(offered, schemas, progReach, reader.Mints(), reader)
	return TurnAuthority{
		Metas:        metas,
		Schemas:      schemas,
		Offered:      offered,
		Plan:         plan,
		ProgramReach: progReach,
		Reach:        reach,
		Reader:       reader,
		Digest:       digest,
	}
}
