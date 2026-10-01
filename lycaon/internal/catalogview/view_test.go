package catalogview_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/approvalregistry"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func deviceCatalog(t *testing.T) (*extpacks.EffectiveCatalog, string) {
	t.Helper()
	root := configlayout.FindModuleRoot()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   content,
		Desired: extpacks.EmptyDesired(),
	})
	if eff.Revision == "" {
		t.Fatal("empty revision")
	}
	return eff, root
}

func TestBuildParity(t *testing.T) {
	eff, root := deviceCatalog(t)
	view, err := catalogview.Build(t.Context(), root, eff)
	testutil.FailErr(t, "Build", err)

	policy, err := hintregistry.ListEffectiveWithCatalog(eff)
	testutil.FailErr(t, "ListEffectiveWithCatalog", err)
	if len(view.Policy) != len(policy) {
		t.Fatalf("policy entries %d want %d", len(view.Policy), len(policy))
	}
	wantCodes := map[string]struct{}{}
	for _, e := range policy {
		wantCodes[e.Code] = struct{}{}
	}
	for _, e := range view.Policy {
		if _, ok := wantCodes[e.Code]; !ok {
			t.Fatalf("unexpected policy code %s", e.Code)
		}
	}

	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "LoadToolProfilesWithCatalog", err)
	if len(view.ToolProfiles) != len(profiles) {
		t.Fatalf("tool profiles %d want %d", len(view.ToolProfiles), len(profiles))
	}
	wantProf := map[string]struct{}{}
	for _, p := range profiles {
		wantProf[p.ID] = struct{}{}
	}
	for _, p := range view.ToolProfiles {
		if _, ok := wantProf[p.ID]; !ok {
			t.Fatalf("unexpected tool profile %s", p.ID)
		}
	}

	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	testutil.FailErr(t, "LoadAgentProfilesEffectiveWithCatalog", err)
	if len(view.AgentProfiles) != len(agents) {
		t.Fatalf("agents %d want %d", len(view.AgentProfiles), len(agents))
	}
	wantAgents := map[string]struct{}{}
	for _, a := range agents {
		wantAgents[a.ID] = struct{}{}
	}
	for _, a := range view.AgentProfiles {
		if _, ok := wantAgents[a.ID]; !ok {
			t.Fatalf("unexpected agent %s", a.ID)
		}
	}

	approvals, err := approvalregistry.ListEffectiveWithCatalog(eff)
	testutil.FailErr(t, "ListEffectiveWithCatalog approvals", err)
	if len(view.Approvals) != len(approvals) {
		t.Fatalf("approvals %d want %d", len(view.Approvals), len(approvals))
	}
	wantAppr := map[string]struct{}{}
	for _, e := range approvals {
		wantAppr[e.Key] = struct{}{}
	}
	for _, e := range view.Approvals {
		if _, ok := wantAppr[e.Key]; !ok {
			t.Fatalf("unexpected approval key %s", e.Key)
		}
	}

	contract, err := prompts.LoadPersonaContract()
	testutil.FailErr(t, "LoadPersonaContract", err)
	pb, err := prompts.LoadPlaybookMatcherEffectiveWithCatalog(eff, contract)
	testutil.FailErr(t, "LoadPlaybookMatcherEffectiveWithCatalog", err)
	wantPB := pb.PlaybookIDs()
	gotPB := view.Playbooks.PlaybookIDs()
	if len(gotPB) != len(wantPB) {
		t.Fatalf("playbooks %d want %d", len(gotPB), len(wantPB))
	}
	for i := range wantPB {
		if gotPB[i] != wantPB[i] {
			t.Fatalf("playbook[%d]=%s want %s", i, gotPB[i], wantPB[i])
		}
	}

	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)
	if len(view.ToolSchemas.Tools) != len(schemas.Tools) {
		t.Fatalf("schemas %d want %d", len(view.ToolSchemas.Tools), len(schemas.Tools))
	}
	for name := range schemas.Tools {
		if _, ok := view.ToolSchemas.Tools[name]; !ok {
			t.Fatalf("missing schema %s", name)
		}
	}

	if view.Rules == nil || view.Rules.Len() == 0 {
		t.Fatal("expected non-empty ruleset")
	}
	if view.Anchors == nil {
		t.Fatal("expected anchors registry")
	}
}

func TestCacheIdentity(t *testing.T) {
	content, desired, root := cacheFixtureCatalog(t)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: desired})
	cache := catalogview.NewCache(root, nil)
	v1, err := cache.For(t.Context(), eff)
	testutil.FailErr(t, "For first", err)
	v2, err := cache.For(t.Context(), eff)
	testutil.FailErr(t, "For second", err)
	if v1 != v2 {
		t.Fatal("same revision must return the same *View pointer")
	}
	if cache.BuildCount() != 1 {
		t.Fatalf("builds=%d want 1", cache.BuildCount())
	}

	eff2 := cacheFixtureRevision(t, content, desired, 1)
	if eff2.Revision == eff.Revision {
		t.Fatal("disabled unit must change revision")
	}
	v3, err := cache.For(t.Context(), eff2)
	testutil.FailErr(t, "For disabled", err)
	if v3 == v1 {
		t.Fatal("different revision must not reuse view pointer")
	}
	if cache.BuildCount() != 2 {
		t.Fatalf("builds=%d want 2", cache.BuildCount())
	}

	// LRU capacity: 9 distinct catalogs ⇒ holds 8.
	cache2 := catalogview.NewCache(root, nil)
	var first *extpacks.EffectiveCatalog
	for i := 0; i < catalogview.LRUCap+1; i++ {
		e := cacheFixtureRevision(t, content, desired, i)
		if i == 0 {
			first = e
		}
		_, err := cache2.For(t.Context(), e)
		testutil.FailErr(t, "For lru", err)
	}
	if cache2.Len() != catalogview.LRUCap {
		t.Fatalf("lru len=%d want %d", cache2.Len(), catalogview.LRUCap)
	}
	if cache2.BuildCount() != catalogview.LRUCap+1 {
		t.Fatalf("builds=%d want %d", cache2.BuildCount(), catalogview.LRUCap+1)
	}
	// Evicted first entry rebuilds.
	before := cache2.BuildCount()
	_, err = cache2.For(t.Context(), first)
	testutil.FailErr(t, "For evicted", err)
	if cache2.BuildCount() != before+1 {
		t.Fatalf("evicted key should rebuild: builds=%d", cache2.BuildCount())
	}

	// Concurrent For on one key builds once.
	cache3 := catalogview.NewCache(root, nil)
	var wg sync.WaitGroup
	var pointers [16]*catalogview.View
	for i := 0; i < len(pointers); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := cache3.For(t.Context(), eff)
			if err != nil {
				t.Errorf("concurrent For: %v", err)
				return
			}
			pointers[i] = v
		}(i)
	}
	wg.Wait()
	if cache3.BuildCount() != 1 {
		t.Fatalf("concurrent builds=%d want 1", cache3.BuildCount())
	}
	for i := 1; i < len(pointers); i++ {
		if pointers[i] != pointers[0] {
			t.Fatal("concurrent For must share one view")
		}
	}
}

func cacheFixtureCatalog(t *testing.T) ([]extpacks.PackContent, extpacks.DesiredState, string) {
	t.Helper()
	dir := t.TempDir()
	writePack(t, dir, "cache", "fixture/cache", map[string]string{
		"policy/FIXTURE_CACHE.yaml": "oar: '1.0'\nid: FIXTURE_CACHE\nkind: policy\nanchor: tool.post_invoke\n" +
			"requires:\n  profiles: [tool]\nwhen: 'false'\neffect: warn\n" +
			"copy:\n  cause: c\n  why: w\n  fix: f\n  instead: i\n",
		"approvals/fixture/explain.yaml":        "approval_explanations:\n  fixture: {}\n",
		"tools/profiles/fixture.yaml":           "id: fixture\ntools: {}\n",
		"agents/fixture.yaml":                   "id: fixture\nname: Fixture\ntool_profile: fixture\ntopology_roles: [coordinator]\n",
		"agents/prompts/_persona-contract.yaml": "version: 1\narchetypes:\n  fixture: {}\nagents:\n  fixture:\n    archetype: fixture\n",
		"playbooks/fixture.yaml":                "id: fixture\nfallback: true\n",
	})
	content := []extpacks.PackContent{inventoryPack(t, filepath.Join(dir, "cache"), "fixture/cache")}
	desired := extpacks.EmptyDesired()
	desired.Packs = []extpacks.DesiredPack{{ID: "fixture/cache"}}
	return content, desired, configlayout.FindModuleRoot()
}

func cacheFixtureRevision(t *testing.T, content []extpacks.PackContent, desired extpacks.DesiredState, index int) *extpacks.EffectiveCatalog {
	t.Helper()
	// Disabled unit IDs contribute to the revision even without a matching unit.
	desired.Disabled = []string{fmt.Sprintf("guidance/fixture-%d", index)}
	return extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: desired})
}

func TestFailureIsTotal(t *testing.T) {
	root := configlayout.FindModuleRoot()
	stock, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)

	dir := t.TempDir()
	writeDupBindingPacks(t, dir)
	a := inventoryPack(t, filepath.Join(dir, "pack-a"), "acme/bindings-a")
	b := inventoryPack(t, filepath.Join(dir, "pack-b"), "acme/bindings-b")
	packs := append(append([]extpacks.PackContent{}, stock...), a, b)
	desired := extpacks.EmptyDesired()
	desired.Packs = []extpacks.DesiredPack{{ID: "acme/bindings-a"}, {ID: "acme/bindings-b"}}
	bad := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: packs, Desired: desired,
	})

	cache := catalogview.NewCache(root, nil)
	_, err = cache.For(t.Context(), bad)
	if err == nil {
		t.Fatal("duplicate binding key must fail Build")
	}
	if cache.Len() != 0 {
		t.Fatalf("failed compile must not serve a view: len=%d", cache.Len())
	}
	if cache.BuildCount() != 0 {
		t.Fatalf("failed build counted as success: %d", cache.BuildCount())
	}
	if cache.AttemptCount() != 1 {
		t.Fatalf("first compile attempts=%d want 1", cache.AttemptCount())
	}

	// Same revision returns the compile error without rebuilding.
	_, err = cache.For(t.Context(), bad)
	if err == nil {
		t.Fatal("retry must still fail")
	}
	if cache.Len() != 0 {
		t.Fatal("failed compile must not serve a view")
	}
	if cache.AttemptCount() != 1 {
		t.Fatalf("same revision rebuilt: attempts=%d want 1", cache.AttemptCount())
	}

	good, _ := deviceCatalog(t)
	v, err := cache.For(t.Context(), good)
	testutil.FailErr(t, "good For", err)
	if v == nil {
		t.Fatal("nil view")
	}
	if cache.Len() != 1 || cache.BuildCount() != 1 {
		t.Fatalf("good catalog: len=%d builds=%d", cache.Len(), cache.BuildCount())
	}
}

func TestForNilCatalogErrors(t *testing.T) {
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), nil)
	_, err := cache.For(context.Background(), nil)
	if err == nil {
		t.Fatal("nil eff must error")
	}
	_, err = catalogview.Build(context.Background(), configlayout.FindModuleRoot(), nil)
	if err == nil {
		t.Fatal("Build nil eff must error")
	}
}

func TestWithRulesPreservesCatalogView(t *testing.T) {
	eff, root := deviceCatalog(t)
	view, err := catalogview.Build(t.Context(), root, eff)
	testutil.FailErr(t, "Build", err)
	originalRules := view.Rules
	replacement := oar.NewRuleSet(nil)

	configured := view.WithRules(replacement)
	if configured == view {
		t.Fatal("expected distinct view")
	}
	if view.Rules != originalRules {
		t.Fatal("source rules changed")
	}
	if configured.Rules != replacement {
		t.Fatal("replacement rules missing")
	}
	if configured.Catalog != view.Catalog || configured.Anchors != view.Anchors {
		t.Fatal("catalog registries changed")
	}
}

// Bundled catalogs do not depend on a checkout config tree.
func TestBuildDoesNotRequireCheckoutConfigTree(t *testing.T) {
	eff, _ := deviceCatalog(t)
	view, err := catalogview.Build(t.Context(), filepath.Join(t.TempDir(), "no-config-tree"), eff)
	testutil.FailErr(t, "Build without checkout config", err)
	if view == nil || view.Rules == nil || view.Anchors == nil {
		t.Fatal("expected populated view from bundled catalog")
	}
	if len(view.AgentProfiles) == 0 {
		t.Fatal("expected agent profiles from stock packs")
	}
}

const widgetYAML = `id: fixture_widget
provider_id: fixture
tool_name: widget
schema:
  type: object
  properties:
    status: { type: string }
fields:
  - key: widget_status
    type: string
    path: /status
`

const dupKeyYAML = `id: dup_widget
provider_id: fixture
tool_name: dup
schema:
  type: object
  properties:
    status: { type: string }
fields:
  - key: widget_status
    type: string
    path: /status
`

func writeDupBindingPacks(t *testing.T, dir string) {
	t.Helper()
	writePack(t, dir, "pack-a", "acme/bindings-a", map[string]string{
		"mcp_bindings/fixture_widget.yaml": widgetYAML,
	})
	writePack(t, dir, "pack-b", "acme/bindings-b", map[string]string{
		"mcp_bindings/dup_widget.yaml": dupKeyYAML,
	})
}

func writePack(t *testing.T, root, leaf, id string, files map[string]string) {
	t.Helper()
	packRoot := filepath.Join(root, leaf)
	if err := os.MkdirAll(packRoot, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	man := "manifest_version: 1\nid: " + id + "\nname: " + id +
		"\nversion: 1.0.0\ncompatibility:\n  extension_api: \"^1.0.0\"\n"
	if err := os.WriteFile(filepath.Join(packRoot, "extension.yaml"), []byte(man), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	for rel, body := range files {
		path := filepath.Join(packRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
}

func inventoryPack(t *testing.T, root, id string) extpacks.PackContent {
	t.Helper()
	man, err := extpacks.LoadManifest(root)
	testutil.FailErr(t, "LoadManifest", err)
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: id, Root: extpacks.OnDisk(root)}, man)
	testutil.FailErr(t, "InventoryPack", err)
	pc.Kind = extpacks.PackKindPath
	return pc
}
