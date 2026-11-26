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

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
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
	{id: "agent-pairings", packs: []string{"agent-pairings"}, worker: true},
	{
		id: "disable-mock-before-build", disable: []string{"skills/mock-before-build"},
		coordinator: true, worker: true, workers: []string{"implementer"},
	},
}

// CustomizationOverlay is a customized effective catalog with the recipients it
// applies to.
type CustomizationOverlay struct {
	Overlay     PackOverlay
	Coordinator bool
	Worker      bool
	// Workers are the worker agents whose facts the customization moves.
	Workers []WorkerAgent
}

func customizationRoot() string {
	return filepath.Join(configlayout.FindModuleRoot(), "test", "contract", "internal", "hoststate", "testdata", "customization")
}

var loadCustomizationOverlays = sync.OnceValues(func() ([]CustomizationOverlay, error) {
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
		co := CustomizationOverlay{Overlay: ov, Coordinator: spec.coordinator, Worker: spec.worker}
		if spec.worker {
			co.Workers = customizedWorkerAgents(stock, ov, spec.workers)
			if len(co.Workers) == 0 {
				return nil, fmt.Errorf("customization %s moves no worker agent", spec.id)
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
	packs := slices.Clone(stockContent)
	desired := extpacks.DesiredState{Format: extpacks.DesiredFormat, Own: spec.own, Disabled: spec.disable}
	for _, dir := range spec.packs {
		root := filepath.Join(customizationRoot(), "packs", dir)
		man, err := extpacks.LoadManifest(root)
		if err != nil {
			return PackOverlay{}, fmt.Errorf("customization %s: %w", spec.id, err)
		}
		pc, err := extpacks.InventoryPack(extpacks.Pack{ID: man.ID, Root: extpacks.OnDisk(root)}, man)
		if err != nil {
			return PackOverlay{}, fmt.Errorf("customization %s: %w", spec.id, err)
		}
		pc.Kind = extpacks.PackKindPath
		packs = append(packs, pc)
		enabled := true
		desired.Packs = append(desired.Packs, extpacks.DesiredPack{ID: man.ID, Source: root, Development: true, Enabled: &enabled})
	}
	return buildPackOverlay(ctx, spec.id, "", packs, desired)
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
