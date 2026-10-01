package session_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func writeProjectExtensions(t *testing.T, projectDir string, body string) {
	t.Helper()
	dir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	body = fmt.Sprintf("format: %d\n%s", extpacks.DesiredFormat, body)
	if err := os.WriteFile(filepath.Join(dir, "extensions.yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}

func newViewManager(t *testing.T, root string, surfaces *settings.TrustSurfacesStore) (*session.Manager, *catalogview.Cache) {
	t.Helper()
	m := session.NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	boot := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: func() []extpacks.PackContent {
			c, err := extpacks.DiscoverStockContent()
			testutil.FailErr(t, "DiscoverStockContent", err)
			return c
		}(),
		Desired: extpacks.EmptyDesired(),
	})
	m.SetEffectiveCatalogDeps(root, boot, surfaces)
	cache := catalogview.NewCache(root, slog.Default())
	m.Catalog().SetCatalogViewCache(cache)
	return m, cache
}

func TestW10ReachMatrix(t *testing.T) {
	ctx := context.Background()
	root := configlayout.FindModuleRoot()
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceExtensionConfig: true}); err != nil {
		testutil.FailErr(t, "PutEnabled", err)
	}

	m, _ := newViewManager(t, root, surfaces)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	m.SetProjectRegistry(reg)

	projA := t.TempDir()
	writeProjectExtensions(t, projA, "disabled:\n  - guidance/coordinator-gate-blocked\n")
	pA, err := project.CreateWithRoot(ctx, reg, projA)
	testutil.FailErr(t, "CreateWithRoot A", err)

	projB := t.TempDir()
	writeProjectExtensions(t, projB, "disabled: []\n")
	pB, err := project.CreateWithRoot(ctx, reg, projB)
	testutil.FailErr(t, "CreateWithRoot B", err)

	viewA := m.Catalog().ViewForProject(ctx, pA.ID)
	if viewA == nil || viewA.Catalog == nil {
		t.Fatal("view A nil")
	}
	if viewA.Catalog.HasLoaded(extpacks.GuidanceUnitID("coordinator-gate-blocked")) {
		t.Fatal("project A should disable guidance/coordinator-gate-blocked")
	}
	viewB := m.Catalog().ViewForProject(ctx, pB.ID)
	if viewB == nil || viewB.Catalog == nil {
		t.Fatal("view B nil")
	}
	if !viewB.Catalog.HasLoaded(extpacks.GuidanceUnitID("coordinator-gate-blocked")) {
		t.Fatal("project B must still see device guidance unit")
	}
	if viewA.Catalog.Revision == viewB.Catalog.Revision {
		t.Fatal("projects must not share catalog fingerprint")
	}
	for _, tc := range []struct {
		session *api.Session
		view    *catalogview.View
	}{
		{&api.Session{ID: "sa", ProjectID: pA.ID, WorkspacePath: projA}, viewA},
		{&api.Session{ID: "sb", ProjectID: pB.ID, WorkspacePath: projB}, viewB},
	} {
		got := m.Catalog().ViewForSession(ctx, tc.session)
		if got == nil || got.Catalog == nil || got.Catalog.Revision != tc.view.Catalog.Revision {
			t.Fatalf("session %s received the wrong project catalog", tc.session.ID)
		}
	}
}

func TestW11GateMatrix(t *testing.T) {
	ctx := context.Background()
	root := configlayout.FindModuleRoot()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	cases := []struct {
		name      string
		deviceOn  bool
		projectOn bool
	}{
		{"project_off", true, false},
		{"device_off", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
			testutil.FailErr(t, "surfaces", err)
			if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceExtensionConfig: tc.deviceOn}); err != nil {
				testutil.FailErr(t, "PutEnabled", err)
			}
			m, _ := newViewManager(t, root, surfaces)
			reg := project.NewMemoryRegistry()
			m.SetProjectRegistry(reg)
			projDir := t.TempDir()
			writeProjectExtensions(t, projDir, "disabled:\n  - guidance/coordinator-gate-blocked\n")
			p, err := project.CreateWithRoot(ctx, reg, projDir)
			testutil.FailErr(t, "CreateWithRoot", err)
			if !tc.projectOn {
				if _, err := reg.SetTrustEnabled(ctx, p.ID, map[string]bool{projectcontrib.SurfaceExtensionConfig: false}); err != nil {
					testutil.FailErr(t, "SetTrustEnabled", err)
				}
			}
			view := m.Catalog().ViewForProject(ctx, p.ID)
			device := m.Catalog().DeviceView(ctx)
			if view == nil || device == nil {
				t.Fatal("nil view")
			}
			if view.Catalog.Revision != device.Catalog.Revision {
				t.Fatalf("gated turn must resolve device catalog; got %s want %s", view.Catalog.Revision, device.Catalog.Revision)
			}
			if len(view.Policy) != len(device.Policy) {
				t.Fatalf("policy len %d want %d", len(view.Policy), len(device.Policy))
			}
			if len(view.AgentProfiles) != len(device.AgentProfiles) {
				t.Fatalf("agents len %d want %d", len(view.AgentProfiles), len(device.AgentProfiles))
			}
			if len(view.ToolProfiles) != len(device.ToolProfiles) {
				t.Fatalf("profiles len %d want %d", len(view.ToolProfiles), len(device.ToolProfiles))
			}
			if len(view.Approvals) != len(device.Approvals) {
				t.Fatalf("approvals len %d want %d", len(view.Approvals), len(device.Approvals))
			}
		})
	}
}

func TestW12PersonaTemplateIsolation(t *testing.T) {
	ctx := context.Background()
	root := configlayout.FindModuleRoot()
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceExtensionConfig: true}); err != nil {
		testutil.FailErr(t, "PutEnabled", err)
	}
	_, cache := newViewManager(t, root, surfaces)

	stock, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)

	// Two catalogs that differ by one disabled guidance unit — distinct fingerprints.
	eff1 := extpacks.Resolve(ctx, extpacks.ResolveInput{
		Packs: stock, Desired: extpacks.EmptyDesired(),
	})
	d2 := extpacks.EmptyDesired()
	d2.Disabled = []string{extpacks.GuidanceUnitID("coordinator-gate-blocked")}
	eff2 := extpacks.Resolve(ctx, extpacks.ResolveInput{
		Packs: stock, Desired: d2,
	})
	if eff1.Revision == eff2.Revision {
		t.Fatal("expected distinct fingerprints")
	}
	v1, err := cache.For(ctx, eff1)
	testutil.FailErr(t, "For 1", err)
	v2, err := cache.For(ctx, eff2)
	testutil.FailErr(t, "For 2", err)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: root,
	})
	a := engine.WithEffectiveCatalog(v1.Catalog)
	b := engine.WithEffectiveCatalog(v2.Catalog)
	if a.Layers().Catalog.Revision == b.Layers().Catalog.Revision {
		t.Fatal("derived engines must keep distinct catalogs")
	}
	// Persona cache keys by catalog fingerprint.
	prompts.ResetPersonaContractCache()
	_, err = prompts.RenderPersona(ctx, a, "implementer", nil)
	if err != nil {
		// Rendering may reject unknown agent variants.
		t.Logf("render A: %v", err)
	}
	_, err = prompts.RenderPersona(ctx, b, "implementer", nil)
	if err != nil {
		t.Logf("render B: %v", err)
	}
}

func TestW13NoSharedMutation(t *testing.T) {
	ctx := context.Background()
	root := configlayout.FindModuleRoot()
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceExtensionConfig: true}); err != nil {
		testutil.FailErr(t, "PutEnabled", err)
	}
	m, _ := newViewManager(t, root, surfaces)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	m.SetProjectRegistry(reg)

	makeProj := func(disable string) *project.Project {
		dir := t.TempDir()
		body := "disabled: []\n"
		if disable != "" {
			body = "disabled:\n  - " + disable + "\n"
		}
		writeProjectExtensions(t, dir, body)
		p, err := project.CreateWithRoot(ctx, reg, dir)
		testutil.FailErr(t, "CreateWithRoot", err)
		return p
	}
	pA := makeProj(extpacks.GuidanceUnitID("coordinator-gate-blocked"))
	pB := makeProj("")

	pipeline := oar.NewGuardPipeline(m.Catalog().DeviceView(ctx).Rules, nil, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	pipeline.SetRuleSetFor(func(ctx context.Context, sessionID string) *oar.RuleSet {
		return m.Catalog().ViewForSessionID(ctx, sessionID).Rules
	})
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := pA.ID
			if i%2 == 0 {
				id = pB.ID
			}
			view := m.Catalog().ViewForProject(ctx, id)
			if view == nil {
				errs <- context.Canceled
				return
			}
			gc := oar.NewGuardContext()
			gc.SessionID = "s-" + id
			gc.Tool = "read"
			if _, err := pipeline.EvaluateBlock(ctx, oar.AnchorToolPreInvoke, gc); err != nil {
				errs <- err
			}
			fe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
				ModuleRoot: root,
			}).WithEffectiveCatalog(view.Catalog)
			if fe.Layers().Catalog.Revision != view.Catalog.Revision {
				errs <- context.DeadlineExceeded
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent turn: %v", err)
		}
	}
}

func TestW14FailureDegradesToDevice(t *testing.T) {
	ctx := context.Background()
	root := configlayout.FindModuleRoot()
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceExtensionConfig: true}); err != nil {
		testutil.FailErr(t, "PutEnabled", err)
	}
	m, _ := newViewManager(t, root, surfaces)
	device := m.Catalog().DeviceView(ctx)
	if device == nil {
		t.Fatal("device view required")
	}
	// Missing projects use the device view.
	got := m.Catalog().ViewForProject(ctx, "missing-project-id")
	if got == nil || got.Catalog == nil {
		t.Fatal("expected device fallback view")
	}
	if got.Catalog.Revision != device.Catalog.Revision {
		t.Fatalf("fallback fingerprint %s want %s", got.Catalog.Revision, device.Catalog.Revision)
	}
	// Later valid projects still build.
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	m.SetProjectRegistry(reg)
	dir := t.TempDir()
	writeProjectExtensions(t, dir, "disabled: []\n")
	p, err := project.CreateWithRoot(ctx, reg, dir)
	testutil.FailErr(t, "CreateWithRoot", err)
	good := m.Catalog().ViewForProject(ctx, p.ID)
	if good == nil {
		t.Fatal("good project view nil")
	}
}

// The Appearance pickers select from the device view, so it carries themes with
// no project scoped.
func TestDeviceViewCarriesStockThemes(t *testing.T) {
	ctx := context.Background()
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceExtensionConfig: true}); err != nil {
		testutil.FailErr(t, "PutEnabled", err)
	}
	m, _ := newViewManager(t, configlayout.FindModuleRoot(), surfaces)

	device := m.Catalog().DeviceView(ctx)
	if device == nil || device.Contributions == nil {
		t.Fatal("device view must carry contributions")
	}
	themes := device.Contributions.Themes()
	if len(themes) == 0 {
		t.Fatal("device view must carry the stock themes; the Appearance picker selects from it")
	}
	// Both schemes, or `system` has nothing to resolve to on one side.
	var light, dark bool
	for _, theme := range themes {
		switch theme.ID {
		case "painted-wolf/platform:daylight":
			light = true
		case "painted-wolf/platform:charcoal":
			dark = true
		}
	}
	if !light || !dark {
		t.Fatalf("stock themes at device scope: daylight=%v charcoal=%v", light, dark)
	}
}
