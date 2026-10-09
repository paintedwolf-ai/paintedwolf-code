package wiring

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/usernotice"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
)

func examplePackDir(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(configlayout.FindModuleRoot(), "config", "fixtures", "example-packs", name)
}

func TestInstalledPacksReachEngines(t *testing.T) {
	h := BuildForTest(t,
		WithoutCoordinatorLoop(),
		WithInstalledPack(
			examplePackDir(t, "house-rules"),
			examplePackDir(t, "docs-writer"),
			examplePackDir(t, "team-flow"),
			examplePackDir(t, "tracker-facts"),
			examplePackDir(t, "credential-recognition"),
			examplePackDir(t, "onboarding-copy"),
			examplePackDir(t, "editor-surface"),
		),
	)
	ctx := context.Background()
	cat := extpacks.Active()
	if cat == nil {
		t.Fatal("app.Build installed no effective catalog")
	}
	t.Run("host/credential-slots → recognition inspector", func(t *testing.T) {
		view := h.SessionMgr.Catalog.DeviceView(ctx)
		if view == nil || view.CredentialSlots == nil {
			t.Fatal("device view has no credential recognition")
		}
		for tool, args := range map[string]map[string]any{
			"external":      {"SERVICE_ACCESS_VALUE": "fixture"},
			"service_apply": {"document": "SERVICE_ACCESS_VALUE=fixture"},
			"command":       {"command": "servicectl --create -x fixture"},
		} {
			if hits := view.CredentialSlots.Inspect(tool, args); len(hits) != 1 {
				t.Errorf("installed recognition did not reach %s: %d candidates", tool, len(hits))
			}
		}
	})

	t.Run("approvals/rules → approval catalog", func(t *testing.T) {
		rules, _, err := extpacks.LoadEffectiveApprovalRules(cat)
		testutil.FailErr(t, "load approval rules", err)
		for _, r := range rules {
			if r.UnitID == "approvals/rules/generated-ts" {
				return
			}
		}
		t.Fatalf("example/house-rules ships approvals/rules/generated-ts; catalog has %d rules without it", len(rules))
	})

	t.Run("agents → spawn registry", func(t *testing.T) {
		if _, err := h.AgentRegistry.Get("example-docs-writer"); err != nil {
			t.Fatalf("example/docs-writer ships agents/example-docs-writer and it is not spawnable: %v", err)
		}
	})

	// Spawnable is not enough. Without the pack's persona row the agent renders
	// its template body alone, which never names how a leg finishes.
	t.Run("agents/prompts/_persona-contract → rendered persona", func(t *testing.T) {
		engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}).
			WithEffectiveCatalog(cat)
		body, err := prompts.RenderPersona(ctx, engine, "example-docs-writer", map[string]any{})
		testutil.FailErr(t, "render pack agent persona", err)
		for _, want := range []string{"complete_leg", "Example Docs Writer"} {
			if !strings.Contains(body, want) {
				t.Fatalf("rendered persona omits %q; pack agents get the worker protocol through their persona row", want)
			}
		}
	})

	t.Run("tools/profiles → sandbox", func(t *testing.T) {
		profiles, err := sandbox.LoadToolProfilesWithCatalog(cat)
		testutil.FailErr(t, "load tool profiles", err)
		for _, p := range profiles {
			if p.ID == "example_docs_only" {
				return
			}
		}
		t.Fatalf("example/docs-writer ships tools/profiles/example_docs_only; sandbox has %d profiles without it", len(profiles))
	})

	t.Run("workflows → manifest resolver", func(t *testing.T) {
		resolver := workflowcatalog.Resolver{
			CatalogFor: func(context.Context, string, string) *extpacks.EffectiveCatalog { return cat },
		}
		reg, _, err := resolver.Resolve(ctx, "", "")
		testutil.FailErr(t, "resolve workflow manifests", err)
		for _, m := range reg.All() {
			if m.ID == "example-triage" {
				return
			}
		}
		t.Fatalf("example/team-flow ships workflows/example-triage; resolver returned %d manifests without it", len(reg.All()))
	})

	t.Run("workflows/_topologies → orchestrator", func(t *testing.T) {
		// The workflow above binds this topology. An unreachable one fails the run
		// at start with topology_missing rather than at load, so the resolved path
		// itself is asserted.
		path := filepath.ToSlash(orchestration.TopologyPathForID(cat, "example-triage-sweep").String())
		if !strings.Contains(path, "example-packs/team-flow") {
			t.Fatalf("topology resolved to %q, want the installed pack's own file", path)
		}
	})

	t.Run("guidance + shared/partials → prompt layer", func(t *testing.T) {
		layout := prompts.DefaultBundledLayout()
		_, at, err := layout.ReadBundled("guidance/example-house-style")
		testutil.FailErr(t, "resolve guidance ref", err)
		if path := filepath.ToSlash(at.String()); !strings.Contains(path, "example-packs/onboarding-copy") {
			t.Fatalf("guidance ref resolved to %q, want the installed pack's own file", path)
		}
		// The template {% include %}s a partial the same pack ships, so this also
		// covers shared/. A dropped partial fails the render rather than rendering empty.
		_, partialAt, err := layout.ReadBundled("partials/example-evidence-first")
		testutil.FailErr(t, "resolve partial ref", err)
		if partial := filepath.ToSlash(partialAt.String()); !strings.Contains(partial, "example-packs/onboarding-copy") {
			t.Fatalf("partial resolved to %q, want the installed pack's own file", partial)
		}
	})

	t.Run("mcp_bindings → fact catalogue", func(t *testing.T) {
		bindings, diags, err := extpacks.LoadEffectiveBindings(cat)
		testutil.FailErr(t, "load mcp bindings", err)
		for _, b := range bindings {
			if b.ID == "example_issue" {
				return
			}
		}
		t.Fatalf("example/tracker-facts ships mcp_bindings/example_issue; loaded %d bindings without it (diags=%v)", len(bindings), diags)
	})

	t.Run("host/user-notices → notice catalog", func(t *testing.T) {
		cfg, err := usernotice.LoadEffectiveUserNotices(cat)
		testutil.FailErr(t, "load user notices", err)
		if _, ok := cfg.UserNotices["EXAMPLE_ONBOARDING_INCOMPLETE"]; !ok {
			t.Fatalf("example/onboarding-copy ships host/user-notices/EXAMPLE_ONBOARDING_INCOMPLETE; catalog has %d codes without it", len(cfg.UserNotices))
		}
	})

	// The contribution kinds reach Den through the compiled set, so this asks
	// the same object the frame projects from — including the configuration
	// value in force, which is what a pack reads about itself.
	t.Run("contributions/* → compiled set", func(t *testing.T) {
		view := h.SessionMgr.Catalog.DeviceView(ctx)
		if view == nil || view.Contributions == nil {
			t.Fatal("device view carries no compiled contribution set")
		}
		set := view.Contributions
		for _, id := range []string{
			"example/editor-surface:summarize-selection",
			"example/editor-surface:read-tracker-issue",
			"example/editor-surface:perform-create-issue",
			"example/editor-surface:summarize",
			"example/editor-surface:key-summarize-selection",
			"example/editor-surface:editor-context-summarize",
			"example/editor-surface:offer-summarize",
			"example/editor-surface:tracker",
			"example/editor-surface:issues",
			"example/editor-surface:create-issue",
			"example/editor-surface:example-slate",
		} {
			parsed, err := contribution.ParseID(id)
			testutil.FailErr(t, "parse contribution id", err)
			if _, ok := set.Unit(parsed); !ok {
				t.Errorf("example/editor-surface declares %s; the compiled set does not hold it", id)
			}
		}
		// Unset in desired state, so the value in force is the declared default.
		var offer contribution.ConfigurationSetting
		for _, setting := range set.Settings() {
			if setting.Property.ID == "example/editor-surface:offer-summarize" {
				offer = setting
			}
		}
		if offer.Value != true || !offer.Default {
			t.Fatalf("configuration setting = %+v, want the declared default in force", offer)
		}
		if own := set.SettingsForPack("example/editor-surface"); own["offer-summarize"] != true {
			t.Fatalf("pack-scoped settings = %+v, want its own value", own)
		}
	})
}

// Mixed providers without an explicit selection disable the unit.
func TestInstalledPackOverrideStaysOffWithoutOwn(t *testing.T) {
	h := BuildForTest(t, WithInstalledPack(examplePackDir(t, "onboarding-copy")))
	cat := extpacks.Active()
	if cat == nil {
		t.Fatal("app.Build installed no effective catalog")
	}
	_ = h

	const unitID = "approvals/git_commit/explain"
	if cat.HasLoaded(unitID) {
		t.Fatalf("%s has two providers (stock + example/onboarding-copy) and no own — it must not load", unitID)
	}
	if got := len(cat.InspectContributions(unitID)); got != 2 {
		t.Fatalf("expected 2 providers for %s, got %d — the fixture no longer collides with stock", unitID, got)
	}
}
