package hoststate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolscope"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// SurfaceInfo pairs a coordinator surface ID with its catalog declaration.
type SurfaceInfo struct {
	ID      string
	Surface surfacecatalog.Surface
}

// WorkerAgent describes one catalog-derived worker agent and its tool profile.
type WorkerAgent struct {
	ID          string
	ToolProfile string
	ExecMode    sandbox.ExecMode
	Profile     agentdef.Profile
}

// PackOverlay captures one stock or single-pack-removed effective catalog state.
type PackOverlay struct {
	ID             string
	RemovedPackID  string
	Catalog        *extpacks.EffectiveCatalog
	Profiles       []sandbox.ToolProfile
	Schemas        *toolschema.Config
	WorkerAgents   []WorkerAgent
	SpawnAllowlist []string
}

// Postures loads all session postures dynamically from session-postures.yaml
// in deterministic ID order.
func Postures(_ *extpacks.EffectiveCatalog) ([]sessionprofiles.PostureSpec, error) {
	reg, err := sessionprofiles.LoadPostureRegistry()
	if err != nil {
		return nil, fmt.Errorf("load posture registry: %w", err)
	}
	list := reg.List()
	slices.SortFunc(list, func(a, b sessionprofiles.PostureSpec) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return list, nil
}

// CoordinatorSurfaces loads all coordinator surfaces from coordinator-surfaces.yaml.
func CoordinatorSurfaces() ([]SurfaceInfo, error) {
	cat, err := surfacecatalog.Load()
	if err != nil {
		return nil, fmt.Errorf("load surface catalog: %w", err)
	}
	ids := cat.SurfaceIDs()
	out := make([]SurfaceInfo, 0, len(ids))
	for _, id := range ids {
		row, rowErr := cat.Surface(id)
		if rowErr != nil {
			return nil, rowErr
		}
		out = append(out, SurfaceInfo{ID: id, Surface: row})
	}
	return out, nil
}

// CoordinatorSurfaceIDs returns the sorted coordinator surface IDs.
func CoordinatorSurfaceIDs() ([]string, error) {
	surfaces, err := CoordinatorSurfaces()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(surfaces))
	for i, s := range surfaces {
		out[i] = s.ID
	}
	return out, nil
}

// FolderlessSurfaces derives 0-root admissible standalone coordinator surfaces
// from ResolveTurnProfile, CompileToolPlans(0), and NoFolderAllowlist.
func FolderlessSurfaces() ([]string, error) {
	cat, err := surfacecatalog.Load()
	if err != nil {
		return nil, err
	}
	plans0, err := surface.CompileToolPlans(0)
	if err != nil {
		return nil, err
	}
	allow, err := toolscope.NoFolderAllowlist()
	if err != nil {
		return nil, err
	}
	allowSet := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowSet[name] = true
	}

	postures, err := Postures(nil)
	if err != nil {
		return nil, err
	}
	reached, err := resolverReachedSurfaces(postures)
	if err != nil {
		return nil, err
	}

	var out []string
	for _, id := range cat.SurfaceIDs() {
		if !reached[id] {
			continue
		}
		row, rowErr := cat.Surface(id)
		if rowErr != nil {
			return nil, rowErr
		}
		if row.RequiresAgent || !row.ProseFinish {
			continue
		}
		plan := plans0[id]
		if len(plan.ImmediateNames()) == 0 {
			continue
		}
		if !allToolsInSet(plan.AddressableNames(), allowSet) {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

func resolverReachedSurfaces(postures []sessionprofiles.PostureSpec) (map[string]bool, error) {
	reached := make(map[string]bool)
	userTurn := []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}}
	ambientStates := []surface.ImplementSessionState{
		{},
		{WorkersInFlight: 1},
		{WrapupGatesLoaded: true, BatchReadyForSynthesis: true},
	}
	for _, mode := range api.AllSessionModeValues() {
		for _, p := range postures {
			sess := &api.Session{Mode: mode, Posture: p.ID}
			for _, st := range ambientStates {
				tp := surface.ResolveTurnProfile(api.CoordinatorRunContext{}, sess, userTurn, st)
				reached[tp.SurfaceID] = true
			}
		}
	}
	wfReg, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return nil, fmt.Errorf("load workflow registry: %w", err)
	}
	for _, manifest := range wfReg.All() {
		for _, phase := range manifest.PhaseDefs {
			bound := strings.TrimSpace(phase.CoordinatorSurface)
			if bound == "" {
				continue
			}
			tp := surface.ResolveTurnProfile(
				api.CoordinatorRunContext{RunID: "run-1", WorkflowID: manifest.ID, PhaseCoordinatorSurface: bound},
				&api.Session{Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild},
				nil,
			)
			reached[tp.SurfaceID] = true
		}
	}
	return reached, nil
}

func allToolsInSet(names []string, set map[string]bool) bool {
	for _, name := range names {
		if !set[name] {
			return false
		}
	}
	return true
}

// WorkerAgents loads all worker agents and their tool profiles from eff.
func WorkerAgents(eff *extpacks.EffectiveCatalog) ([]WorkerAgent, error) {
	defs, err := agentdef.LoadEffectiveWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("load effective agents: %w", err)
	}
	out := make([]WorkerAgent, 0, len(defs))
	for _, d := range defs {
		if d.ID == "coordinator" || !slices.Contains(d.TopologyRoles, agentdef.TopologyRoleWorker) {
			continue
		}
		out = append(out, WorkerAgent{
			ID:          d.ID,
			ToolProfile: d.ToolProfile,
			ExecMode:    d.ExecMode,
			Profile:     d,
		})
	}
	slices.SortFunc(out, func(a, b WorkerAgent) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

var loadDefaultPackOverlays = sync.OnceValues(func() ([]PackOverlay, error) {
	stock, err := extpacks.DiscoverStockContent()
	if err != nil {
		return nil, fmt.Errorf("discover stock content: %w", err)
	}
	return PackOverlays(context.Background(), stock)
})

// DiscoverPackOverlays discovers stock packs and builds PackOverlays.
func DiscoverPackOverlays(_ context.Context) ([]PackOverlay, error) {
	overlays, err := loadDefaultPackOverlays()
	if err != nil {
		return nil, err
	}
	return slices.Clone(overlays), nil
}

// PackOverlays builds the full pack catalog plus single-pack removals
// dynamically discovered from stockContent (skipping foundational dependency
// roots and theme-only packs with zero behavioral units).
func PackOverlays(ctx context.Context, stockContent []extpacks.PackContent) ([]PackOverlay, error) {
	if len(stockContent) == 0 {
		return nil, fmt.Errorf("stock content required")
	}
	full, err := buildPackOverlay(ctx, "stock", "", extpacks.ResolveInput{Packs: stockContent, Desired: extpacks.EmptyDesired()})
	if err != nil {
		return nil, err
	}
	removable := discoverRemovablePacks(stockContent)
	out := make([]PackOverlay, 0, 1+len(removable))
	out = append(out, full)
	for _, packID := range removable {
		short := packID
		if idx := strings.LastIndexByte(packID, '/'); idx >= 0 {
			short = packID[idx+1:]
		}
		disabledIDs := transitiveReverseDependents(stockContent, packID)
		enabled := false
		desiredPacks := make([]extpacks.DesiredPack, 0, len(disabledIDs))
		for _, id := range disabledIDs {
			desiredPacks = append(desiredPacks, extpacks.DesiredPack{ID: id, Enabled: &enabled})
		}
		desired := extpacks.DesiredState{
			Format: extpacks.DesiredFormat,
			Packs:  desiredPacks,
		}
		overlay, buildErr := buildPackOverlay(ctx, "without-"+short, packID, extpacks.ResolveInput{Packs: stockContent, Desired: desired})
		if buildErr != nil {
			return nil, buildErr
		}
		out = append(out, overlay)
	}
	return out, nil
}

func discoverRemovablePacks(packs []extpacks.PackContent) []string {
	dependedOn := map[string]bool{}
	for _, item := range packs {
		for depID := range item.Manifest.Dependencies {
			dependedOn[depID] = true
		}
	}
	var ids []string
	for _, item := range packs {
		packID := strings.TrimSpace(item.Pack.ID)
		if packID == "" {
			continue
		}
		// Foundational base pack (zero dependencies, required by dependent packs).
		if len(item.Manifest.Dependencies) == 0 && dependedOn[packID] {
			continue
		}
		if !hasBehavioralUnits(item.Units) {
			continue
		}
		ids = append(ids, packID)
	}
	slices.Sort(ids)
	return ids
}

func hasBehavioralUnits(units []extpacks.InventoriedUnit) bool {
	for _, u := range units {
		switch u.Kind {
		case "policy", "guidance", "workflows", "agents", "tools", "skills", "shared", extpacks.NextStepsKindRoot, "mcp_bindings":
			return true
		}
	}
	return false
}

func transitiveReverseDependents(packs []extpacks.PackContent, rootPackID string) []string {
	disabled := map[string]bool{rootPackID: true}
	changed := true
	for changed {
		changed = false
		for _, item := range packs {
			if disabled[item.Pack.ID] {
				continue
			}
			for dep := range item.Manifest.Dependencies {
				if disabled[dep] {
					disabled[item.Pack.ID] = true
					changed = true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(disabled))
	for id := range disabled {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func buildPackOverlay(
	ctx context.Context,
	id, removedID string,
	in extpacks.ResolveInput,
) (PackOverlay, error) {
	eff := extpacks.Resolve(ctx, in)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return PackOverlay{}, fmt.Errorf("overlay %s profiles: %w", id, err)
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return PackOverlay{}, fmt.Errorf("overlay %s schemas: %w", id, err)
	}
	workers, err := WorkerAgents(eff)
	if err != nil {
		return PackOverlay{}, fmt.Errorf("overlay %s workers: %w", id, err)
	}
	present := make(map[string]bool, len(workers))
	for _, w := range workers {
		present[w.ID] = true
	}
	var allowlist []string
	for _, candidate := range spawn.AmbientAllowedAgents() {
		if present[candidate] && !slices.Contains(allowlist, candidate) {
			allowlist = append(allowlist, candidate)
		}
	}
	for _, w := range workers {
		unit, hasUnit := eff.Loaded["agents/"+w.ID]
		isContributed := hasUnit && !eff.StockAuthority(unit.WinnerPackID)
		if (isContributed || w.ToolProfile == "web_research") && !slices.Contains(allowlist, w.ID) {
			allowlist = append(allowlist, w.ID)
		}
	}
	return PackOverlay{
		ID:             id,
		RemovedPackID:  removedID,
		Catalog:        eff,
		Profiles:       profiles,
		Schemas:        schemas,
		WorkerAgents:   workers,
		SpawnAllowlist: allowlist,
	}, nil
}
