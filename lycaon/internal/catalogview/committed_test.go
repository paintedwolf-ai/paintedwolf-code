package catalogview_test

import (
	"errors"
	"github.com/lycaon/lycaon/internal/configlayout"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fixturePack(t *testing.T, id string, files map[string]string) extpacks.PackContent {
	t.Helper()
	root := filepath.Join(t.TempDir(), strings.ReplaceAll(id, "/", "-"))
	manifest := "manifest_version: 1\nid: " + id + "\nname: " + id +
		"\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n"
	testutil.FailErr(t, "mkdir pack", os.MkdirAll(root, 0o755))
	testutil.FailErr(t, "write manifest", os.WriteFile(filepath.Join(root, "extension.yaml"), []byte(manifest), 0o644))
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir unit dir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write unit", os.WriteFile(path, []byte(body), 0o644))
	}
	man, err := extpacks.LoadManifest(root)
	testutil.FailErr(t, "load manifest", err)
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: id, Root: extpacks.OnDisk(root)}, man)
	testutil.FailErr(t, "inventory pack", err)
	return pc
}

func resolveWithFixtures(t *testing.T, fixtures ...extpacks.PackContent) *extpacks.EffectiveCatalog {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	desired := extpacks.EmptyDesired()
	for _, pc := range fixtures {
		if !extpacks.IsStockPackID(pc.Pack.ID) {
			desired.Packs = append(desired.Packs, extpacks.DesiredPack{ID: pc.Pack.ID})
		}
	}
	return extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   append(content, fixtures...),
		Desired: desired,
	})
}

func TestBuildCompilesContributionsIntoView(t *testing.T) {
	reviewer := fixturePack(t, "acme/reviewer", map[string]string{
		"contributions/commands/explain-selection.yaml": "id: acme/reviewer:explain-selection\ntitle: Explain\naction:\n  kind: navigate\n  destination: home\n",
	})
	eff := resolveWithFixtures(t, reviewer)
	view, err := catalogview.Build(t.Context(), configlayout.FindModuleRoot(), eff)
	testutil.FailErr(t, "Build", err)
	if view.Contributions == nil {
		t.Fatal("view has no compiled contributions")
	}
	// Assert the fixture independently of stock catalog size.
	unit, ok := view.Contributions.Unit(contribution.ID{Provider: "acme/reviewer", Name: "explain-selection"})
	if !ok || unit.Kind != contribution.KindCommand || !strings.Contains(string(unit.Body), "title: Explain") {
		t.Fatalf("compiled unit = %+v ok=%v", unit, ok)
	}
}

// Candidate polarity: any contribution fault rejects the complete view build.
func TestBuildRejectsCandidateWithContributionFault(t *testing.T) {
	imposter := fixturePack(t, "acme/imposter", map[string]string{
		"contributions/commands/steal.yaml": "id: acme/reviewer:steal\n",
	})
	eff := resolveWithFixtures(t, imposter)
	_, err := catalogview.Build(t.Context(), configlayout.FindModuleRoot(), eff)
	var compileErr *contribution.CompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("Build error = %v, want CompileError", err)
	}
}

// A faulting pack is omitted; the rest compile.
func TestForCommittedOmitsFaultingPack(t *testing.T) {
	good := fixturePack(t, "acme/good", map[string]string{
		"contributions/commands/fine.yaml": "id: acme/good:fine\ntitle: Fine\naction:\n  kind: navigate\n  destination: home\n",
	})
	bad := fixturePack(t, "acme/bad", map[string]string{
		"contributions/commands/steal.yaml": "id: acme/good:steal\n",
	})
	eff := resolveWithFixtures(t, good, bad)

	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	view, committed, err := cache.ForCommitted(t.Context(), eff)
	testutil.FailErr(t, "ForCommitted", err)
	if committed == eff {
		t.Fatal("committed catalog must be the omitted successor")
	}
	if _, ok := view.Contributions.Unit(contribution.ID{Provider: "acme/good", Name: "fine"}); !ok {
		t.Fatal("good pack contribution must survive omission")
	}
	if _, ok := view.Contributions.Unit(contribution.ID{Provider: "acme/good", Name: "steal"}); ok {
		t.Fatal("the faulting pack's unit must not reach the compiled set")
	}
	if committed.PackContributed("acme/bad") {
		t.Fatal("faulting pack must not contribute")
	}
	var blocked extpacks.PackSummary
	for _, sum := range committed.Packs {
		if sum.ID == "acme/bad" {
			blocked = sum
		}
	}
	if blocked.BlockedReason != extpacks.BlockedInvalid {
		t.Fatalf("blocked reason = %q want invalid", blocked.BlockedReason)
	}
	found := false
	for _, d := range committed.Diagnostics {
		if d.Code == extpacks.DiagPackInvalid && d.PackID == "acme/bad" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected pack_invalid diagnostic")
	}
	testutil.FailErr(t, "boot after omission", committed.BootError())
}

// Dependents follow their omitted dependency through ordinary gating.
func TestForCommittedOmissionGatesDependents(t *testing.T) {
	bad := fixturePack(t, "acme/base", map[string]string{
		"contributions/commands/steal.yaml": "id: acme/other:steal\n",
	})
	dependentRoot := filepath.Join(t.TempDir(), "dependent")
	manifest := "manifest_version: 1\nid: acme/dependent\nname: Dependent\nversion: 1.0.0\n" +
		"compatibility:\n  extension_api: ^1.0.0\ndependencies:\n  acme/base:\n    source: path:/dev/null\n    version: '*'\n"
	testutil.FailErr(t, "mkdir dependent", os.MkdirAll(filepath.Join(dependentRoot, "guidance"), 0o755))
	testutil.FailErr(t, "write dependent manifest", os.WriteFile(filepath.Join(dependentRoot, "extension.yaml"), []byte(manifest), 0o644))
	testutil.FailErr(t, "write dependent unit", os.WriteFile(filepath.Join(dependentRoot, "guidance", "dep-note.md"), []byte("note\n"), 0o644))
	man, err := extpacks.LoadManifest(dependentRoot)
	testutil.FailErr(t, "load dependent manifest", err)
	dependent, err := extpacks.InventoryPack(extpacks.Pack{ID: "acme/dependent", Root: extpacks.OnDisk(dependentRoot)}, man)
	testutil.FailErr(t, "inventory dependent", err)

	eff := resolveWithFixtures(t, bad, dependent)
	if !eff.PackContributed("acme/dependent") {
		t.Fatal("dependent must contribute before omission")
	}
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	_, committed, err := cache.ForCommitted(t.Context(), eff)
	testutil.FailErr(t, "ForCommitted", err)
	if committed.PackContributed("acme/base") || committed.PackContributed("acme/dependent") {
		t.Fatal("omitted pack and its dependent must both stop contributing")
	}
	if committed.HasLoaded("guidance/dep-note") {
		t.Fatal("dependent unit must drop with its dependency")
	}
}

// Bundled stock faults are fatal; an on-disk namespace claim omits.
func TestForCommittedStockFaultFailsLoud(t *testing.T) {
	stockish := fixturePack(t, "painted-wolf/broken-fixture", map[string]string{
		"contributions/commands/steal.yaml": "id: acme/other:steal\n",
	})
	eff := resolveWithFixtures(t, stockish)
	for i := range eff.Packs {
		if eff.Packs[i].ID == "painted-wolf/broken-fixture" {
			eff.Packs[i].Bundled = true
		}
	}
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	_, _, err := cache.ForCommitted(t.Context(), eff)
	var compileErr *contribution.CompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("bundled stock fault must fail loudly with the compile error, got %v", err)
	}
}

// An imposter without the bundled trust root gets non-stock treatment.
func TestForCommittedStockNamespaceImposterOmits(t *testing.T) {
	imposter := fixturePack(t, "painted-wolf/imposter", map[string]string{
		"contributions/commands/steal.yaml": "id: acme/other:steal\n",
	})
	eff := resolveWithFixtures(t, imposter)
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	_, committed, err := cache.ForCommitted(t.Context(), eff)
	testutil.FailErr(t, "ForCommitted", err)
	if committed.PackContributed("painted-wolf/imposter") {
		t.Fatal("a namespace imposter must omit, not crash the catalog")
	}
	if eff.StockAuthority("painted-wolf/imposter") {
		t.Fatal("an on-disk pack must never hold stock authority")
	}
}

// Isolation trials carry their own revisions; caching them would evict real
// views from the shared LRU. Only the final admitted set may enter the cache.
func TestReadmitTrialsDoNotEnterSharedCache(t *testing.T) {
	good := fixturePack(t, "acme/good", map[string]string{
		"contributions/commands/fine.yaml": "id: acme/good:fine\ntitle: Fine\naction:\n  kind: navigate\n  destination: home\n",
	})
	broken := fixturePack(t, "acme/broken", map[string]string{
		"policy/BROKEN_POLICY.yaml": "oar: '1.0'\nid: BROKEN_POLICY\nkind: policy\nanchor: tool.pre_invoke\nwhen: '1'\neffect: block\n",
	})
	eff := resolveWithFixtures(t, good, broken)

	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	_, committed, err := cache.ForCommitted(t.Context(), eff)
	testutil.FailErr(t, "ForCommitted", err)
	if cache.Len() != 1 {
		t.Fatalf("cache len = %d want 1: isolation trials must not enter the shared LRU", cache.Len())
	}
	if cache.BuildCount() != 1 {
		t.Fatalf("cached builds = %d want 1 (the final admitted view only)", cache.BuildCount())
	}
	// The one cached entry is the final committed view.
	before := cache.BuildCount()
	_, err = cache.For(t.Context(), committed)
	testutil.FailErr(t, "For committed", err)
	if cache.BuildCount() != before {
		t.Fatal("final committed view must already be cached")
	}
}

// Catalog views use bytes captured at resolution.
func TestBuildServesCapturedBytesAfterEdit(t *testing.T) {
	const policyBody = "oar: '1.0'\nid: FIXTURE_CAPTURED\nkind: policy\nanchor: tool.post_invoke\n" +
		"requires:\n  profiles:\n    - tool\nwhen: 'false'\neffect: warn\n" +
		"copy:\n  cause: c\n  why: w\n  fix: f\n  instead: i\n" +
		"x-paintedwolf-emit: rule:test\nx-paintedwolf-message: m\n"
	pack := fixturePack(t, "acme/captured", map[string]string{
		"policy/FIXTURE_CAPTURED.yaml": policyBody,
		"agents/fixture-agent.yaml":    "id: fixture-agent\nname: Fixture\ntool_profile: read_only\n",
		"agents/prompts/_persona-contract.yaml": "version: 2\nagents:\n  fixture-agent:\n" +
			"    archetype: explore_readonly\n    playbook_ids: [worker-leg-default]\n" +
			"    delta:\n      role_title: Fixture\n      focus: f\n      process: [p]\n",
		"agents/prompts/fixture-agent.md": "fixture\n",
	})
	eff := resolveWithFixtures(t, pack)

	// On-disk edits do not affect the captured catalog.
	for _, rel := range []string{"policy/FIXTURE_CAPTURED.yaml", "agents/fixture-agent.yaml"} {
		at, ok := eff.UnitPath("policy/FIXTURE_CAPTURED")
		if rel == "agents/fixture-agent.yaml" {
			at, ok = eff.UnitPath("agents/fixture-agent")
		}
		if !ok {
			t.Fatalf("unit path for %s not found", rel)
		}
		testutil.FailErr(t, "corrupt "+rel, os.WriteFile(at.String(), []byte("oar: '1.0'\nid: BROKEN_POLICY\nkind: policy\nanchor: tool.pre_invoke\nwhen: '1'\neffect: block\n"), 0o644))
	}

	view, err := catalogview.Build(t.Context(), configlayout.FindModuleRoot(), eff)
	testutil.FailErr(t, "Build after on-disk edit", err)

	foundPolicy := false
	for _, e := range view.Policy {
		if e.Code == "FIXTURE_CAPTURED" {
			foundPolicy = true
			if !strings.Contains(string(e.Body), "FIXTURE_CAPTURED") || strings.Contains(string(e.Body), "unclosed") {
				t.Fatalf("policy body must be the captured bytes, got %q", e.Body)
			}
		}
	}
	if !foundPolicy {
		t.Fatal("captured policy unit missing from view")
	}
	if _, err := view.Get("fixture-agent"); err != nil {
		t.Fatalf("captured agent profile missing from view: %v", err)
	}
}

// An unattributed loader failure isolates deterministically to its pack.
func TestForCommittedIsolatesUnattributedFailure(t *testing.T) {
	good := fixturePack(t, "acme/good", map[string]string{
		"contributions/commands/fine.yaml": "id: acme/good:fine\ntitle: Fine\naction:\n  kind: navigate\n  destination: home\n",
	})
	broken := fixturePack(t, "acme/broken", map[string]string{
		"policy/BROKEN_POLICY.yaml": "oar: '1.0'\nid: BROKEN_POLICY\nkind: policy\nanchor: tool.pre_invoke\nwhen: '1'\neffect: block\n",
	})
	eff := resolveWithFixtures(t, good, broken)

	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	view, committed, err := cache.ForCommitted(t.Context(), eff)
	testutil.FailErr(t, "ForCommitted", err)
	if committed.PackContributed("acme/broken") {
		t.Fatal("broken pack must omit")
	}
	if !committed.PackContributed("acme/good") {
		t.Fatal("good pack must keep contributing")
	}
	if _, ok := view.Contributions.Unit(contribution.ID{Provider: "acme/good", Name: "fine"}); !ok {
		t.Fatal("good pack contribution must survive")
	}
	found := false
	for _, d := range committed.Diagnostics {
		if d.Code == extpacks.DiagPackInvalid && d.PackID == "acme/broken" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected pack_invalid diagnostic for acme/broken")
	}
}

func TestForCandidateKeepsTheErrorForAProtectedPack(t *testing.T) {
	broken := fixturePack(t, "acme/broken", map[string]string{
		"contributions/commands/go.yaml": "id: acme/broken:go\ntitle: Go\naction:\n  kind: navigate\n  destination: nowhere\n",
	})
	eff := resolveWithFixtures(t, broken)
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.DiscardHandler))
	if _, _, err := cache.ForCandidate(t.Context(), eff, []string{"acme/broken"}); err == nil {
		t.Fatal("protecting the failing pack must keep its fault")
	}
}

func TestForCandidateIsolatesAPackTheOperatorDidNotName(t *testing.T) {
	broken := fixturePack(t, "acme/broken", map[string]string{
		"contributions/commands/go.yaml": "id: acme/broken:go\ntitle: Go\naction:\n  kind: navigate\n  destination: nowhere\n",
	})
	fine := fixturePack(t, "acme/fine", map[string]string{
		"contributions/commands/ok.yaml": "id: acme/fine:ok\ntitle: OK\naction:\n  kind: navigate\n  destination: home\n",
	})
	eff := resolveWithFixtures(t, broken, fine)
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.DiscardHandler))
	view, committed, err := cache.ForCandidate(t.Context(), eff, []string{"acme/fine"})
	testutil.FailErr(t, "ForCandidate", err)
	if _, ok := view.Contributions.Unit(contribution.ID{Provider: "acme/fine", Name: "ok"}); !ok {
		t.Fatal("the named pack must still contribute")
	}
	if !heldOut(committed, "acme/broken") {
		t.Fatalf("acme/broken should be held out and reported: %+v", committed.Diagnostics)
	}
}

func TestInForceReportsAHeldOutPackAsBlocked(t *testing.T) {
	broken := fixturePack(t, "acme/broken", map[string]string{
		"contributions/commands/go.yaml": "id: acme/broken:go\ntitle: Go\naction:\n  kind: navigate\n  destination: nowhere\n",
	})
	eff := resolveWithFixtures(t, broken)
	for _, p := range eff.Packs {
		if p.ID == "acme/broken" && !p.Contributing {
			t.Skip("resolve already blocked the pack; this test is about the view build")
		}
	}
	views := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.DiscardHandler))
	if !heldOut(views.InForce(t.Context(), eff), "acme/broken") {
		t.Fatal("InForce must report the pack the view build holds out")
	}
}

func heldOut(eff *extpacks.EffectiveCatalog, packID string) bool {
	for _, p := range eff.Packs {
		if p.ID != packID {
			continue
		}
		if p.Contributing || p.BlockedReason != extpacks.BlockedInvalid {
			return false
		}
	}
	for _, d := range eff.Diagnostics {
		if d.PackID == packID && d.Code == extpacks.DiagPackInvalid {
			return true
		}
	}
	return false
}
