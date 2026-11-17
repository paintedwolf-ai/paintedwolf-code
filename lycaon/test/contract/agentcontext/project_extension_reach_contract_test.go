package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func scanInventoryKindRoots(t *testing.T) []string {
	t.Helper()
	src := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon/internal/extpacks/inventory.go")
	start := strings.Index(src, "var inventoryKindRoots = []string{")
	if start < 0 {
		t.Fatal("inventoryKindRoots not found in inventory.go")
	}
	end := strings.Index(src[start:], "}")
	if end < 0 {
		t.Fatal("unterminated inventoryKindRoots")
	}
	var roots []string
	for _, line := range strings.Split(src[start:start+end], "\n")[1:] {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		switch {
		case strings.HasPrefix(line, `"`):
			roots = append(roots, strings.Trim(line, `"`))
		case line == "ArchiveKindRoot":
			roots = append(roots, extpacks.ArchiveKindRoot)
		case line == "NextStepsKindRoot":
			roots = append(roots, extpacks.NextStepsKindRoot)
		case line == "DetectionPackKindRoot":
			roots = append(roots, extpacks.DetectionPackKindRoot)
		case line == "CredentialSlotsKindRoot":
			roots = append(roots, extpacks.CredentialSlotsKindRoot)
		}
	}
	if len(roots) == 0 {
		t.Fatal("no kind roots parsed from inventoryKindRoots")
	}
	return roots
}

func TestProjectScopeCoversEveryKindRootContract(t *testing.T) {
	t.Parallel()
	kindRoots := scanInventoryKindRoots(t)
	for _, root := range kindRoots {
		if extpacks.ProjectScope(root) == 0 {
			t.Fatalf("kind root %q has no ProjectScope class", root)
		}
	}
	var deviceOnly []string
	for _, root := range kindRoots {
		if extpacks.ProjectScope(root) == extpacks.ScopeDeviceOnly {
			deviceOnly = append(deviceOnly, root)
		}
	}
	if len(deviceOnly) == 0 {
		t.Fatal("device-only class must be non-empty")
	}
	foundTools := false
	for _, k := range deviceOnly {
		if k == "tools" {
			foundTools = true
			break
		}
	}
	if !foundTools {
		t.Fatalf("device-only class must include tools; got %v", deviceOnly)
	}
}

func TestDesiredStateAPIHasOneProvenanceShape(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sources := contractcheck.ReadRepoFile(t, root, "lycaon/internal/extpacks/desired.go") +
		contractcheck.ReadRepoFile(t, root, "lycaon/internal/extpacks/desired_write.go")
	for _, forbidden := range []string{
		"func MergeDesiredWithProvenance(",
		"func LoadMergedDesiredWithProvenance(",
		"func LoadDesiredFileChecked(",
	} {
		if strings.Contains(sources, forbidden) {
			t.Fatalf("desired-state API retains obsolete alternate shape %q", forbidden)
		}
	}
}

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

func TestCatalogViewCacheBoundAndFingerprintKey(t *testing.T) {
	t.Parallel()
	root := configlayout.FindModuleRoot()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "DiscoverStockContent", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: stock, Desired: extpacks.EmptyDesired(),
	})
	if eff.Revision == "" {
		t.Fatal("empty fingerprint")
	}
	if catalogview.LRUCap != 8 {
		t.Fatalf("LRUCap=%d want 8", catalogview.LRUCap)
	}

	cache := catalogview.NewCache(root, nil)
	v1, err := cache.For(t.Context(), eff)
	contractcheck.FailErr(t, "For first", err)
	v2, err := cache.For(t.Context(), eff)
	contractcheck.FailErr(t, "For second", err)
	if v1 != v2 {
		t.Fatal("same fingerprint must return the same *View pointer")
	}
	if cache.BuildCount() != 1 {
		t.Fatalf("builds=%d want 1", cache.BuildCount())
	}

	// LRU hard cap.
	cache2 := catalogview.NewCache(root, nil)
	var first *extpacks.EffectiveCatalog
	for i := 0; i < catalogview.LRUCap+1; i++ {
		e := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
			Packs: stock, Desired: uniqueDisableDesired(i),
		})
		if i == 0 {
			first = e
		}
		_, err := cache2.For(t.Context(), e)
		contractcheck.FailErr(t, "For lru", err)
	}
	if cache2.Len() != catalogview.LRUCap {
		t.Fatalf("lru len=%d want %d", cache2.Len(), catalogview.LRUCap)
	}
	before := cache2.BuildCount()
	_, err = cache2.For(t.Context(), first)
	contractcheck.FailErr(t, "For evicted", err)
	if cache2.BuildCount() != before+1 {
		t.Fatalf("evicted key should rebuild: builds=%d", cache2.BuildCount())
	}

	// Failed builds are not cached.
	failCache := catalogview.NewCache("", nil)
	_, err = failCache.For(t.Context(), eff)
	if err == nil {
		t.Fatal("empty module root must fail Build")
	}
	if failCache.Len() != 0 {
		t.Fatalf("failed build must not cache: len=%d", failCache.Len())
	}
	if failCache.BuildCount() != 0 {
		t.Fatalf("failed build counted as success: %d", failCache.BuildCount())
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
			m := session.NewHost(store.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
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

func uniqueDisableDesired(i int) extpacks.DesiredState {
	d := extpacks.EmptyDesired()
	switch i % 9 {
	case 0:
	case 1:
		d.Disabled = []string{extpacks.GuidanceUnitID("coordinator-gate-blocked")}
	case 2:
		d.Disabled = []string{extpacks.PolicyUnitID("WRITE_SCOPE_DENIED")}
	case 3:
		d.Disabled = []string{extpacks.PolicyUnitID("DOOM_LOOP_REPEAT")}
	case 4:
		d.Disabled = []string{extpacks.PolicyUnitID("DOOM_LOOP_REPEAT_WARN")}
	case 5:
		d.Disabled = []string{extpacks.WorkflowUnitID("plan")}
	case 6:
		d.Disabled = []string{extpacks.WorkflowUnitID("implement")}
	case 7:
		d.Disabled = []string{extpacks.GuidanceUnitID("coordinator-gate-blocked"), extpacks.PolicyUnitID("WRITE_SCOPE_DENIED")}
	default:
		d.Disabled = []string{extpacks.PolicyUnitID("TOOL_PROFILE_DENIED")}
	}
	return d
}
