package hoststate

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
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

var (
	executorCacheMu sync.Mutex
	executorCache   = map[executorCacheKey]*toolexecution.Executor{}
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
	narrowed, err := NarrowProfilesForPosture(overlay.Profiles, posture, clampReadOnly)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	sandboxCfg, err := sandbox.LoadConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load sandbox config: %w", err)
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

	executorCacheMu.Lock()
	executorCache[key] = exec
	executorCacheMu.Unlock()
	return exec, narrowed, nil
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
// native program reach, and Reader for a CoordinatorState.
func FreezeTurnAuthority(ctx context.Context, state CoordinatorState) (TurnAuthority, error) {
	overlay := state.Overlay
	if overlay.Catalog == nil {
		overlays, err := DiscoverPackOverlays(ctx)
		if err != nil {
			return TurnAuthority{}, err
		}
		overlay = overlays[0]
	}
	exec, narrowed, err := ExecutorForOverlay(
		overlay, state.Posture, false, state.WebSearch, state.CommandExternalHidden, state.MCPState,
	)
	if err != nil {
		return TurnAuthority{}, err
	}

	surfaceID := state.TurnProfile.SurfaceID
	filter := platform.ToolFilter{
		ProfileID:        prompts.CoordinatorProfileID,
		SurfaceID:        surfaceID,
		ProjectRootCount: state.RootCount,
	}
	rawMetas, err := exec.Metadata.List(ctx, filter)
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("Metadata.List: %w", err)
	}
	plan := applyCoordinatorRuntimePlan(state, rawMetas)
	metas := filterCoordinatorTurnMetas(state, plan, rawMetas)

	progReach, err := exec.Metadata.ProgramReach(ctx, filter)
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("Metadata.ProgramReach: %w", err)
	}

	offered := toolMetaNames(metas)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: surfaceID,
		Mode:      surface.ExecutionModeFamily(surfaceID),
		Profiles:  narrowed,
		Plan:      plan,
		Loaded:    state.Loaded,
		Visible:   offered,
		Schemas:   overlay.Schemas,
		RootCount: state.RootCount,
		HasGit:    state.HasGit,
		WebSearch: state.WebSearch,
	})
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("prompts.NewReader: %w", err)
	}
	reader = reader.WithVision(state.Vision)

	return assembleTurnAuthority(metas, offered, plan, progReach, reader), nil
}

func applyCoordinatorRuntimePlan(state CoordinatorState, rawMetas []tools.ToolMeta) toolsurface.Plan {
	plan := state.Plan
	if !state.WebSearch {
		plan = plan.Without(webresearch.SearchToolName, webresearch.FetchURLToolName)
	}
	for name, ok := range state.Loaded {
		if ok && plan.Addressable(name) {
			plan = plan.Promote(name)
		}
	}
	if plan.Immediate("request_tools") {
		for _, meta := range rawMetas {
			if meta.IsMCP() && (state.SessionMode != api.SessionModeAsk || tools.AllowedInAsk(meta)) {
				plan = plan.Promote(meta.Name)
			}
		}
	}
	if state.SessionMode == api.SessionModeAsk {
		allowed := make(map[string]bool, len(rawMetas))
		for _, m := range rawMetas {
			allowed[m.Name] = tools.AllowedInAsk(m)
		}
		for _, name := range plan.AddressableNames() {
			if !allowed[name] {
				plan = plan.Without(name)
			}
		}
		if !state.TurnProfile.WorkflowActive {
			plan = plan.Without("ask_user")
		}
	}
	return plan
}

func filterCoordinatorTurnMetas(
	state CoordinatorState,
	plan toolsurface.Plan,
	rawMetas []tools.ToolMeta,
) []tools.ToolMeta {
	metas := make([]tools.ToolMeta, 0, len(rawMetas))
	for _, meta := range rawMetas {
		if !toolcontract.AdmitsSession(meta.Name, false) && !meta.IsMCP() {
			continue
		}
		if state.SessionMode == api.SessionModeAsk && !tools.AllowedInAsk(meta) {
			continue
		}
		metas = append(metas, meta)
	}
	if state.SessionMode == api.SessionModeAsk {
		metas = promptloop.ProjectAskToolMetas(metas)
	}
	out := make([]tools.ToolMeta, 0, len(metas))
	for _, meta := range metas {
		if !plan.Immediate(meta.Name) {
			continue
		}
		projected := tools.ProjectProviderToolMeta(meta)
		switch meta.Name {
		case "task":
			if state.SpawnAllowlist != nil && len(state.SpawnAllowlist) == 0 {
				continue
			}
			projected = guard.ApplySpawnAllowlistSchema(projected, state.SpawnAllowlist)
		case "instruct_worker":
			projected = guard.ApplySpawnAllowlistSchema(projected, state.SpawnAllowlist)
		}
		out = append(out, projected)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
