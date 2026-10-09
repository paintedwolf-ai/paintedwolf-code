package assembly

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectPromptCatalogIsolation(t *testing.T) {
	stock, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "discover catalog", err)
	unit := extpacks.GuidanceUnitID("coordinator-gate-blocked")
	disabled := extpacks.EmptyDesired()
	disabled.Disabled = []string{unit}
	catalogs := map[string]*extpacks.EffectiveCatalog{
		"a": extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: disabled}),
		"b": extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: extpacks.EmptyDesired()}),
	}
	base := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		Prompts: base,
		SessionView: func(_ context.Context, sess *api.Session) *catalogview.View {
			return &catalogview.View{Catalog: catalogs[sess.ProjectID]}
		},
		ProjectOverlayRootPaths: func(context.Context, *api.Session) []string { return nil },
	})
	for _, projectID := range []string{"a", "b", "a"} {
		sess := &api.Session{ID: "session-" + projectID, ProjectID: projectID}
		derived, ok := testPromptSurface(engine).projectPrompts(t.Context(), sess).(*prompts.FileTemplateEngine)
		if !ok || derived == nil {
			t.Fatal("expected project prompt engine")
		}
		catalog := derived.Layers().Catalog
		if catalog != catalogs[projectID] || catalog.HasLoaded(unit) != (projectID == "b") {
			t.Fatalf("project %s received the wrong catalog", projectID)
		}
	}
	if base.Layers().Catalog != nil {
		t.Fatal("project catalog mutated the shared prompt engine")
	}
}
