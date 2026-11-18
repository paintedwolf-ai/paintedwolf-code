package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestFloorProjectDisables(t *testing.T) {
	t.Parallel()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "DiscoverStockContent", err)
	dir := t.TempDir()
	writeContractFixturePack(t, dir, "acme-add", extpacks.Manifest{
		ID:            "acme/add",
		Name:          "add",
		Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		Dependencies:  map[string]extpacks.DependencyRequest{"painted-wolf/platform": {Version: "*"}},
	}, map[string]string{
		"policy/HOUSE_RULE.yaml": "spec_version: '1.0'\nid: HOUSE_RULE\nkind: policy\non: tool.pre_invoke\neffect: warn\n",
		"approvals/house.yaml":   "title: house approval\n",
	})
	acme := mustContractInventoryPack(t, filepath.Join(dir, "acme-add"), "acme/add")
	packs := append(append([]extpacks.PackContent{}, stock...), acme)
	merged, prov := extpacks.MergeDesired(extpacks.EmptyDesired(), []string{
		extpacks.PolicyUnitID("WRITE_SCOPE_DENIED"),
	})
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: packs, Desired: merged, Provenance: prov,
	})
	if !eff.HasLoaded(extpacks.PolicyUnitID("WRITE_SCOPE_DENIED")) {
		t.Fatal("stock policy must stay loaded when project disables it")
	}
	if !hasFloorDiagUnit(eff, extpacks.DiagProjectScopeRefused, extpacks.PolicyUnitID("WRITE_SCOPE_DENIED")) {
		t.Fatal("expected project_scope_refused for stock policy disable")
	}

	merged, prov = extpacks.MergeDesired(extpacks.EmptyDesired(), []string{"policy/HOUSE_RULE"})
	eff = extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: packs, Desired: merged, Provenance: prov,
	})
	if eff.HasLoaded("policy/HOUSE_RULE") {
		t.Fatal("project may disable a policy unit only its pack provides")
	}
	if hasFloorDiagUnit(eff, extpacks.DiagProjectScopeRefused, "policy/HOUSE_RULE") {
		t.Fatal("no floor diagnostic when dropping the project's own unit")
	}
}

func TestGateParitySurfaceOffResolvesDevice(t *testing.T) {
	ctx := context.Background()
	root := configlayout.FindModuleRoot()

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
			contractcheck.FailErr(t, "surfaces", err)
			if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceExtensionConfig: tc.deviceOn}); err != nil {
				contractcheck.FailErr(t, "PutEnabled", err)
			}
			boot := extpacks.Resolve(ctx, extpacks.ResolveInput{
				Packs: func() []extpacks.PackContent {
					c, err := extpacks.DiscoverStockContent()
					contractcheck.FailErr(t, "DiscoverStockContent", err)
					return c
				}(),
				Desired: extpacks.EmptyDesired(),
			})
			m := session.NewHost(store.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())
			m.SetEffectiveCatalogDeps(root, boot, surfaces)
			m.Catalog.SetCatalogViewCache(catalogview.NewCache(root, nil))

			reg := project.NewMemoryRegistry()
			m.SetProjectRegistry(reg)
			projDir := t.TempDir()
			lycaonDir := filepath.Join(projDir, settingsoverlay.DirName())
			contractcheck.FailErr(t, "mkdir", os.MkdirAll(lycaonDir, 0o755))
			contractcheck.FailErr(t, "write extensions", os.WriteFile(
				filepath.Join(lycaonDir, "extensions.yaml"),
				[]byte("disabled:\n  - guidance/coordinator-gate-blocked\n"),
				0o644,
			))
			p, err := project.CreateWithRoot(ctx, reg, projDir)
			contractcheck.FailErr(t, "CreateWithRoot", err)
			if !tc.projectOn {
				if _, err := reg.SetTrustEnabled(ctx, p.ID, map[string]bool{projectcontrib.SurfaceExtensionConfig: false}); err != nil {
					contractcheck.FailErr(t, "SetTrustEnabled", err)
				}
			}
			view := m.Catalog.ViewForProject(ctx, p.ID)
			device := m.Catalog.DeviceView(ctx)
			if view == nil || device == nil {
				t.Fatal("nil view")
			}
			if view.Catalog.Revision != device.Catalog.Revision {
				t.Fatalf("gated turn must resolve device catalog; got %s want %s", view.Catalog.Revision, device.Catalog.Revision)
			}
			// Consumers of the view (policy / agents / tools / approvals) must match device.
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

func hasFloorDiagUnit(eff *extpacks.EffectiveCatalog, code, unitID string) bool {
	if eff == nil {
		return false
	}
	for _, d := range eff.Diagnostics {
		if d.Code == code && d.UnitID == unitID {
			return true
		}
	}
	return false
}

func writeContractFixturePack(t *testing.T, root, leaf string, man extpacks.Manifest, files map[string]string) {
	t.Helper()
	packRoot := filepath.Join(root, leaf)
	contractcheck.FailErr(t, "mkdir pack", os.MkdirAll(packRoot, 0o755))
	data, err := yaml.Marshal(man)
	contractcheck.FailErr(t, "marshal manifest", err)
	contractcheck.FailErr(t, "write extension.yaml", os.WriteFile(filepath.Join(packRoot, "extension.yaml"), data, 0o644))
	for rel, body := range files {
		path := filepath.Join(packRoot, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "mkdir unit", os.MkdirAll(filepath.Dir(path), 0o755))
		contractcheck.FailErr(t, "write unit", os.WriteFile(path, []byte(body), 0o644))
	}
}

func mustContractInventoryPack(t *testing.T, packRoot, id string) extpacks.PackContent {
	t.Helper()
	man, err := extpacks.LoadManifest(packRoot)
	if err != nil {
		man = extpacks.Manifest{
			ID:            id,
			Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		}
	}
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: id, Root: extpacks.OnDisk(packRoot)}, man)
	contractcheck.FailErr(t, "InventoryPack", err)
	return pc
}
