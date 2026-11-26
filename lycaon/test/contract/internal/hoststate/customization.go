package hoststate

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// customizationSpec is one device customization of the stock catalog: fixture
// packs installed from a folder plus the desired-state own and disabled rows.
// Tools and skills are device-only kinds, so these customizations are device
// scope; agents and workflows may also arrive at project scope.
type customizationSpec struct {
	id      string
	packs   []string
	own     map[string]string
	disable []string
	// coordinator and worker select the recipients whose reader facts the
	// customization moves.
	coordinator bool
	worker      bool
	// workers names stock agents whose facts move without a profile change.
	workers []string
	// packRoots are pack folders installed beside packs, by absolute path.
	packRoots []string
	// project is a fixture project whose overlay extensions file disables
	// units at project scope.
	project string
	// coordinatorSeeds and workerSeeds replace the default states the
	// customization meets.
	coordinatorSeeds func() ([]CustomizationSeed, error)
	workerSeeds      []WorkerSeed
}

var customizationSpecs = []customizationSpec{
	{
		id: "own-coordinator-without-dispatch", packs: []string{"coordinator-without-dispatch"},
		own:         map[string]string{"tools/profiles/coordinator": "fixture/coordinator-without-dispatch"},
		coordinator: true,
	},
	{
		id: "own-coordinator-without-inline-work", packs: []string{"coordinator-without-inline-work"},
		own:         map[string]string{"tools/profiles/coordinator": "fixture/coordinator-without-inline-work"},
		coordinator: true,
	},
	{
		id: "own-implement-without-coordination", packs: []string{"implement-without-coordination"},
		own:    map[string]string{"tools/profiles/implement": "fixture/implement-without-coordination"},
		worker: true,
	},
	{
		id: "own-implement-without-command", packs: []string{"implement-without-command"},
		own:    map[string]string{"tools/profiles/implement": "fixture/implement-without-command"},
		worker: true,
	},
	{
		id: "own-implement-egress-only", packs: []string{"implement-egress-only"},
		own:    map[string]string{"tools/profiles/implement": "fixture/implement-egress-only"},
		worker: true,
	},
	{id: "agent-pairings", packs: []string{"agent-pairings"}, worker: true},
	{
		id: "disable-mock-before-build", disable: []string{"skills/mock-before-build"},
		coordinator: true, worker: true, workers: []string{"implementer"},
	},
}

// CustomizationCase is a customization a consumer adds to the matrix with
// the states it meets: unit disables from device desired state and from a
// fixture project's extensions file, resolved through extpacks.Resolve.
type CustomizationCase struct {
	ID string
	// Packs are pack folders installed from disk; Own selects collision
	// winners among them and the stock packs.
	Packs          []string
	Own            map[string]string
	DeviceDisabled []string
	// Project is a fixture project directory; its overlay extensions.yaml
	// disables units at project scope.
	Project string
	// Coordinator lists the coordinator states the case meets; it runs when
	// the overlays load, so it may read the workflow and surface catalogs.
	Coordinator  func() ([]CustomizationSeed, error)
	WorkerAgents []string
	Workers      []WorkerSeed
}

// CustomizationSeed is one coordinator state a customization case meets,
// with web search on, balanced approvals, scratch temp and no MCP.
type CustomizationSeed struct {
	Mode      api.SessionMode
	Posture   api.SessionPosture
	Workflow  WorkflowState
	SurfaceID string
	Roots     RootTopology
	Loading   LoadingMode
	Vision    bool
	Binding   *PhaseBinding
}

var customizationsLoaded atomic.Bool

// AddCustomizationCases adds cases to the matrix; it must run before the
// first matrix state is built.
func AddCustomizationCases(cases ...CustomizationCase) bool {
	if customizationsLoaded.Load() {
		panic("hoststate: customization cases added after the customization overlays loaded")
	}
	for _, c := range cases {
		customizationSpecs = append(customizationSpecs, customizationSpec{
			id: c.ID, packRoots: c.Packs, own: c.Own, disable: c.DeviceDisabled, project: c.Project,
			coordinator: c.Coordinator != nil, worker: len(c.WorkerAgents) > 0, workers: c.WorkerAgents,
			coordinatorSeeds: c.Coordinator, workerSeeds: c.Workers,
		})
	}
	return true
}

// CustomizationOverlay is a customized effective catalog with the recipients it
// applies to.
type CustomizationOverlay struct {
	Overlay     PackOverlay
	Coordinator bool
	Worker      bool
	// Workers are the worker agents whose facts the customization moves.
	Workers []WorkerAgent
	// CoordinatorSeeds and WorkerSeeds, when set, are the only states the
	// customization meets.
	CoordinatorSeeds []CustomizationSeed
	WorkerSeeds      []WorkerSeed
}

func customizationRoot() string {
	return filepath.Join(configlayout.FindModuleRoot(), "test", "contract", "internal", "hoststate", "testdata", "customization")
}

var loadCustomizationOverlays = sync.OnceValues(func() ([]CustomizationOverlay, error) {
	customizationsLoaded.Store(true)
	ctx := context.Background()
	stockContent, err := extpacks.DiscoverStockContent()
	if err != nil {
		return nil, fmt.Errorf("discover stock content: %w", err)
	}
	base, err := DiscoverPackOverlays(ctx)
	if err != nil {
		return nil, err
	}
	stock := base[0]
	out := make([]CustomizationOverlay, 0, len(customizationSpecs))
	for _, spec := range customizationSpecs {
		ov, err := buildCustomizationOverlay(ctx, spec, stockContent)
		if err != nil {
			return nil, err
		}
		if err := refuseNewErrorDiagnostics(stock.Catalog, ov.Catalog, spec.id); err != nil {
			return nil, err
		}
		co := CustomizationOverlay{Overlay: ov, Coordinator: spec.coordinator, Worker: spec.worker, WorkerSeeds: spec.workerSeeds}
		if spec.coordinatorSeeds != nil {
			if co.CoordinatorSeeds, err = spec.coordinatorSeeds(); err != nil {
				return nil, fmt.Errorf("customization %s: %w", spec.id, err)
			}
			if len(co.CoordinatorSeeds) == 0 {
				return nil, fmt.Errorf("customization %s meets no coordinator state", spec.id)
			}
		}
		if spec.worker {
			co.Workers = customizedWorkerAgents(stock, ov, spec.workers)
			if len(co.Workers) == 0 {
				return nil, fmt.Errorf("customization %s moves no worker agent", spec.id)
			}
			for _, name := range spec.workers {
				if !slices.ContainsFunc(co.Workers, func(a WorkerAgent) bool { return a.ID == name }) {
					return nil, fmt.Errorf("customization %s names worker %s, which its catalog does not load", spec.id, name)
				}
			}
		}
		out = append(out, co)
	}
	return out, nil
})

// CustomizationOverlays returns the device customizations the matrix renders.
func CustomizationOverlays(_ context.Context) ([]CustomizationOverlay, error) {
	out, err := loadCustomizationOverlays()
	if err != nil {
		return nil, err
	}
	return slices.Clone(out), nil
}

func buildCustomizationOverlay(ctx context.Context, spec customizationSpec, stockContent []extpacks.PackContent) (PackOverlay, error) {
	roots := make([]string, 0, len(spec.packs))
	for _, dir := range spec.packs {
		roots = append(roots, filepath.Join(customizationRoot(), "packs", dir))
	}
	roots = append(roots, spec.packRoots...)
	desired := extpacks.DesiredState{Format: extpacks.DesiredFormat, Own: spec.own, Disabled: spec.disable}
	var projectDisabled []string
	if spec.project != "" {
		m, err := extpacks.LoadSuggestionFile(extpacks.ProjectDesiredPath(spec.project))
		if err != nil {
			return PackOverlay{}, fmt.Errorf("customization %s: %w", spec.id, err)
		}
		if len(m.Disabled) == 0 {
			return PackOverlay{}, fmt.Errorf("customization %s: project %s disables nothing", spec.id, spec.project)
		}
		projectDisabled = m.Disabled
	}
	ov, err := overlayWithPacks(ctx, spec.id, roots, "", stockContent, desired, projectDisabled, spec.project)
	if err != nil {
		return PackOverlay{}, err
	}
	for _, id := range slices.Concat(spec.disable, projectDisabled) {
		_, known := ov.Catalog.Units[id]
		_, loaded := ov.Catalog.Loaded[id]
		if !known || loaded {
			return PackOverlay{}, fmt.Errorf("customization %s: Resolve did not disable %s (known=%t loaded=%t)", spec.id, id, known, loaded)
		}
	}
	return ov, nil
}

// overlayWithPacks links each pack folder into the device desired state, as
// installing a folder does; installedFrom names the project that suggested
// them. projectDisabled are the units projectID's extensions file disables.
func overlayWithPacks(
	ctx context.Context,
	id string,
	roots []string,
	installedFrom string,
	stockContent []extpacks.PackContent,
	desired extpacks.DesiredState,
	projectDisabled []string,
	projectID string,
) (PackOverlay, error) {
	packs := slices.Clone(stockContent)
	for _, root := range roots {
		man, err := extpacks.LoadManifest(root)
		if err != nil {
			return PackOverlay{}, fmt.Errorf("customization %s: %w", id, err)
		}
		pc, err := extpacks.InventoryPack(extpacks.Pack{ID: man.ID, Root: extpacks.OnDisk(root)}, man)
		if err != nil {
			return PackOverlay{}, fmt.Errorf("customization %s: %w", id, err)
		}
		pc.Kind = extpacks.PackKindPath
		packs = append(packs, pc)
		enabled := true
		desired.Packs = append(desired.Packs, extpacks.DesiredPack{ID: man.ID, Source: root, Development: true, Enabled: &enabled, InstalledFrom: installedFrom})
	}
	merged, provenance := extpacks.MergeDesired(desired, projectDisabled)
	return buildPackOverlay(ctx, id, "", extpacks.ResolveInput{Packs: packs, Desired: merged, ProjectID: projectID, Provenance: provenance})
}

// ProjectAgentOverlay is the stock device catalog with the pack at root
// installed as a project's suggestion. Agents are a kind a project shares
// (extpacks ScopeShared); their tool profiles resolve on the device catalog.
// Workers are the agents the pack adds.
func ProjectAgentOverlay(ctx context.Context, id, root, projectID string) (CustomizationOverlay, error) {
	stockContent, err := extpacks.DiscoverStockContent()
	if err != nil {
		return CustomizationOverlay{}, fmt.Errorf("discover stock content: %w", err)
	}
	base, err := DiscoverPackOverlays(ctx)
	if err != nil {
		return CustomizationOverlay{}, err
	}
	desired := extpacks.DesiredState{Format: extpacks.DesiredFormat, Own: map[string]string{}}
	ov, err := overlayWithPacks(ctx, id, []string{root}, projectID, stockContent, desired, nil, "")
	if err != nil {
		return CustomizationOverlay{}, err
	}
	if err := refuseNewErrorDiagnostics(base[0].Catalog, ov.Catalog, id); err != nil {
		return CustomizationOverlay{}, err
	}
	workers := customizedWorkerAgents(base[0], ov, nil)
	if len(workers) == 0 {
		return CustomizationOverlay{}, fmt.Errorf("project pack %s adds no worker agent", root)
	}
	return CustomizationOverlay{Overlay: ov, Worker: true, Workers: workers}, nil
}

// refuseNewErrorDiagnostics fails when Resolve reports something the
// customization asked for as not in effect, which production would refuse.
func refuseNewErrorDiagnostics(stock, custom *extpacks.EffectiveCatalog, id string) error {
	key := func(d extpacks.Diagnostic) string { return d.Code + "|" + d.UnitID + "|" + d.PackID }
	known := map[string]bool{}
	for _, d := range stock.Diagnostics {
		known[key(d)] = true
	}
	var added []string
	for _, d := range custom.Diagnostics {
		if extpacks.SeverityForCode(d.Code) == extpacks.SeverityError && !known[key(d)] {
			added = append(added, d.Code+" "+d.UnitID+" "+d.PackID+": "+d.Message)
		}
	}
	if len(added) > 0 {
		return fmt.Errorf("customization %s is refused by Resolve:\n%s", id, strings.Join(added, "\n"))
	}
	return nil
}

// customizedWorkerAgents returns the worker agents the stock overlay lacks or
// whose tool profile the customization changed, plus the named agents.
func customizedWorkerAgents(stock, custom PackOverlay, named []string) []WorkerAgent {
	stockAgents := map[string]WorkerAgent{}
	for _, a := range stock.WorkerAgents {
		stockAgents[a.ID] = a
	}
	var out []WorkerAgent
	for _, a := range custom.WorkerAgents {
		prior, inStock := stockAgents[a.ID]
		changed := !inStock || prior.ToolProfile != a.ToolProfile ||
			!reflect.DeepEqual(profileByID(stock, a.ToolProfile), profileByID(custom, a.ToolProfile))
		if changed || slices.Contains(named, a.ID) {
			out = append(out, a)
		}
	}
	return out
}

func profileByID(overlay PackOverlay, id string) any {
	for _, p := range overlay.Profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// profileSurveysProjectTree reports whether profileID in overlay can survey the
// project tree, which production refuses to spawn without project roots.
func profileSurveysProjectTree(overlay PackOverlay, profileID string) bool {
	for _, p := range overlay.Profiles {
		if p.ID == profileID {
			surface, err := prompts.LoadAgentToolSurface(p.ID, nil, nil, nil, nil, []sandbox.ToolProfile{p})
			return err == nil && prompts.ProfileSurveysProjectTree(surface)
		}
	}
	return false
}

// profileMutationCapable reports whether profileID in overlay can mutate.
func profileMutationCapable(overlay PackOverlay, profileID string) bool {
	for _, p := range overlay.Profiles {
		if p.ID == profileID {
			return prompts.ToolProfileMutationCapable(p)
		}
	}
	return false
}

var loadProjectPhaseBindings = sync.OnceValues(func() ([]PhaseBinding, error) {
	ctx := context.Background()
	overlays, err := DiscoverPackOverlays(ctx)
	if err != nil {
		return nil, err
	}
	projectDir := filepath.Join(customizationRoot(), "project")
	reg, scopes, excluded, err := workflowcatalog.Resolver{
		CatalogFor:         func(context.Context, string, string) *extpacks.EffectiveCatalog { return overlays[0].Catalog },
		ProjectTierApplies: func(context.Context, string) bool { return true },
	}.ResolveWithExcluded(ctx, projectDir, "")
	if err != nil {
		return nil, fmt.Errorf("resolve project workflows: %w", err)
	}
	if len(excluded) > 0 {
		return nil, fmt.Errorf("project workflow fixtures were excluded: %+v", excluded)
	}
	manifests := reg.All()
	keys := make([]string, 0, len(manifests))
	for key := range manifests {
		if scopes[key] == string(api.WorkflowScopeProject) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var out []PhaseBinding
	for _, key := range keys {
		m := manifests[key]
		for _, phase := range m.PhaseDefs {
			b, err := BindPhase(m, phase)
			if err != nil {
				return nil, err
			}
			if b.Resolved.CoordinatorSurface == "" {
				return nil, fmt.Errorf("project workflow %s phase %s binds no coordinator surface", m.ID, phase.ID)
			}
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no project workflow bindings resolved from %s", projectDir)
	}
	return out, nil
})

// ProjectPhaseBindings resolves the fixture project's workflow phases through
// the production workflow catalog and ResolveSurfaceBinding: each binds a mode
// or surface template to a surface the shipped workflows do not pair it with.
func ProjectPhaseBindings() ([]PhaseBinding, error) {
	out, err := loadProjectPhaseBindings()
	if err != nil {
		return nil, err
	}
	return slices.Clone(out), nil
}
