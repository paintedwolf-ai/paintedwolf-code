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

var singlePackRemovals = []string{
	"implement",
	"recon-pack",
	"web-research",
	"security-survey",
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
	for _, mode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
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
	if eff == nil {
		content, err := extpacks.DiscoverStockContent()
		if err != nil {
			return nil, err
		}
		eff = extpacks.Resolve(context.Background(), extpacks.ResolveInput{
			Packs:   content,
			Desired: extpacks.EmptyDesired(),
		})
	}
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

// PackOverlays builds the full stock pack catalog plus single-pack removals
// (implement, recon-pack, web-research, security-survey).
func PackOverlays(ctx context.Context, stockContent []extpacks.PackContent) ([]PackOverlay, error) {
	if len(stockContent) == 0 {
		return nil, fmt.Errorf("stock content required")
	}
	full, err := buildPackOverlay(ctx, "stock", "", stockContent, extpacks.EmptyDesired())
	if err != nil {
		return nil, err
	}
	out := []PackOverlay{full}
	for _, short := range singlePackRemovals {
		packID := resolveStockPackID(stockContent, short)
		if packID == "" {
			return nil, fmt.Errorf("stock pack %q not found", short)
		}
		enabled := false
		desired := extpacks.DesiredState{
			Format: extpacks.DesiredFormat,
			Packs:  []extpacks.DesiredPack{{ID: packID, Enabled: &enabled}},
		}
		overlay, buildErr := buildPackOverlay(ctx, "without-"+short, packID, stockContent, desired)
		if buildErr != nil {
			return nil, buildErr
		}
		out = append(out, overlay)
	}
	return out, nil
}

func resolveStockPackID(stock []extpacks.PackContent, short string) string {
	want := "painted-wolf/" + short
	for _, item := range stock {
		if item.Pack.ID == short || item.Pack.ID == want {
			return item.Pack.ID
		}
	}
	return ""
}

func buildPackOverlay(
	ctx context.Context,
	id, removedID string,
	packs []extpacks.PackContent,
	desired extpacks.DesiredState,
) (PackOverlay, error) {
	eff := extpacks.Resolve(ctx, extpacks.ResolveInput{Packs: packs, Desired: desired})
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
		if w.ID == "web-researcher" && !slices.Contains(allowlist, w.ID) {
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
