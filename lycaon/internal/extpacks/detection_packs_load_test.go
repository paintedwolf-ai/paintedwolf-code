package extpacks_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

const destroyRuleYAML = `title: Destroy the fixture
id: 3f2b91a4-6d0e-4a9c-9a1f-0b7c2d5e8a11
description: Fixture rule for the detection contribution surface.
logsource: {product: lycaon, service: tool_exec}
level: critical
detection:
  selection:
    Image: [fixturectl]
  selection_verb:
    CommandLine|contains: ' destroy'
  condition: selection and selection_verb
`

const rotateRuleYAML = `title: Rotate the fixture credential
id: 8c1d4e77-2f30-4b6a-9d55-1e4a7c93b220
description: Second fixture rule so a pack can lose one and keep the other.
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  selection:
    Image: [fixturectl]
  selection_verb:
    CommandLine|contains: ' rotate'
  condition: selection and selection_verb
`

const fixturePackYAML = `id: fixture-cloud
label: Fixture cloud
description: Detection rules contributed by a fixture extension pack.
`

// Shipped and installed detection packs use the same inventory path.
func TestShippedDetectionPacksMatchTheCatalog(t *testing.T) {
	t.Parallel()
	shipped, warnings, err := detectionpack.ShippedPacks()
	testutil.FailErr(t, "ShippedPacks", err)
	if len(warnings) > 0 {
		t.Fatalf("shipped packs carry load warnings: %v", warnings)
	}
	if len(shipped) == 0 {
		t.Fatal("no shipped detection packs found in the tree")
	}

	eff, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	testutil.FailErr(t, "ResolveStockCatalog", err)
	contributed, diags := extpacks.LoadEffectiveDetectionPacks(eff)
	if len(diags) > 0 {
		t.Fatalf("shipped detection packs did not resolve cleanly: %v", diags)
	}

	byID := map[string]detectionpack.Pack{}
	for _, p := range contributed {
		byID[p.ID] = p
	}
	if len(byID) != len(shipped) {
		t.Fatalf("catalog contributed %d packs; the tree holds %d", len(byID), len(shipped))
	}
	for _, want := range shipped {
		got, ok := byID[want.ID]
		if !ok {
			t.Fatalf("pack %s is in the tree but the catalog did not contribute it", want.ID)
		}
		if got.ProviderPackID == "" {
			t.Fatalf("pack %s reached the catalog with no provider pack", want.ID)
		}
		if got.Source != detectionpack.SourceBundled && got.Source != detectionpack.SourceGenerated {
			t.Fatalf("pack %s resolved as source %q; shipped content must stay shipped", want.ID, got.Source)
		}
		if ruleKeys(got) != ruleKeys(want) {
			t.Fatalf("pack %s rules differ\n catalog: %s\n    tree: %s", want.ID, ruleKeys(got), ruleKeys(want))
		}
	}
}

// Stock validation exercises every shipped pack's inventoried fixtures.
func TestShippedDetectionPacksRehearseCleanThroughTheCatalog(t *testing.T) {
	t.Parallel()
	eff, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	testutil.FailErr(t, "ResolveStockCatalog", err)
	semantics, err := detectionpack.LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)
	for _, d := range extpacks.RehearseDetectionPacks(eff, semantics) {
		t.Errorf("%s: %s", d.UnitID, d.Message)
	}
}

func ruleKeys(p detectionpack.Pack) string {
	keys := make([]string, 0, len(p.Rules))
	for _, r := range p.Rules {
		keys = append(keys, r.Slug+"="+r.ID)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func TestDetectionPackUnitIDs(t *testing.T) {
	t.Parallel()
	// Unit ids carry the providing pack, so two packs shipping the same
	// directory name never contend for one id.
	const provider = "acme/kit"
	cases := []struct{ rel, want string }{
		{"host/detection-packs/fixture-cloud/pack.yaml", "host/detection-packs/acme/kit:fixture-cloud/pack"},
		{"host/detection-packs/fixture-cloud/fixtures.yaml", "host/detection-packs/acme/kit:fixture-cloud/fixtures"},
		{"host/detection-packs/fixture-cloud/rules/destroy.yml", "host/detection-packs/acme/kit:fixture-cloud/rules/destroy"},
		// A rule named the way the manifest beside it is named still loads.
		{"host/detection-packs/fixture-cloud/rules/destroy.yaml", "host/detection-packs/acme/kit:fixture-cloud/rules/destroy"},
		// Documentation and drafts are not units.
		{"host/detection-packs/fixture-cloud/README.md", ""},
		{"host/detection-packs/fixture-cloud/rules/drafts/wip.yml", ""},
		{"host/detection-packs/fixture-cloud", ""},
	}
	for _, tc := range cases {
		if got := extpacks.UnitIDFor(provider, tc.rel); got != tc.want {
			t.Errorf("UnitIDFor(%q, %q)=%q want %q", provider, tc.rel, got, tc.want)
		}
	}
}

// A repository may enable a pack the device already has; it may never define a
// rule, at any scope, through any spelling.
func TestDetectionPacksAreDeviceOnly(t *testing.T) {
	t.Parallel()
	if got := extpacks.ProjectScope(extpacks.DetectionPackKindRoot); got != extpacks.ScopeDeviceOnly {
		t.Fatalf("detection packs are project scope class %v; want device-only", got)
	}
	if !extpacks.ProviderScoped(extpacks.DetectionPackKindRoot) {
		t.Fatal("detection packs must refuse own:")
	}
}

// `own:` does not apply to detection units.
func TestOwnRefusedForDetectionUnits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDetectionPack(t, dir, "pack-a", "acme/a", "fixture-cloud", map[string]string{
		"rules/destroy.yml": destroyRuleYAML,
	})
	writeDetectionPack(t, dir, "pack-b", "acme/b", "fixture-cloud", map[string]string{
		"rules/destroy.yml": rotateRuleYAML,
	})
	a := inventoryUnitPack(t, dir, "pack-a", "acme/a")
	b := inventoryUnitPack(t, dir, "pack-b", "acme/b")

	desired := enabledExtensionPacks("acme/a", "acme/b")
	desired.Own = map[string]string{"host/detection-packs/fixture-cloud/rules/destroy": "acme/b"}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a, b},
		Desired: desired,
	})
	if !hasDiagnostic(eff.Diagnostics, extpacks.DiagOwnRefused, "host/detection-packs/fixture-cloud/rules/destroy") {
		t.Fatalf("expected own_refused, got %v", eff.Diagnostics)
	}
	// A refused `own:` entry leaves the unit conflicted.
	if eff.HasLoaded("host/detection-packs/fixture-cloud/rules/destroy") {
		t.Fatal("a contested detection rule must not load")
	}
}

// The loader rejects rules shipped under another pack's directory.
func TestForeignRuleContributionRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDetectionPack(t, dir, "pack-a", "acme/a", "fixture-cloud", map[string]string{
		"rules/destroy.yml": destroyRuleYAML,
	})
	writeDetectionPackRulesOnly(t, dir, "pack-b", "acme/b", "fixture-cloud", map[string]string{
		"rules/rotate.yml": rotateRuleYAML,
	})
	a := inventoryUnitPack(t, dir, "pack-a", "acme/a")
	b := inventoryUnitPack(t, dir, "pack-b", "acme/b")

	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a, b},
		Desired: enabledExtensionPacks("acme/a", "acme/b"),
	})
	packs, diags := extpacks.LoadEffectiveDetectionPacks(eff)
	if len(packs) != 1 {
		t.Fatalf("want one pack, got %+v", packs)
	}
	if got := len(packs[0].Rules); got != 1 {
		t.Fatalf("foreign rule was adopted: pack carries %d rules", got)
	}
	if packs[0].ProviderPackID != "acme/a" {
		t.Fatalf("provider=%q want acme/a", packs[0].ProviderPackID)
	}
	if !hasDiagnostic(diags, extpacks.DiagDetectionPackForeignUnit, "host/detection-packs/acme/b:fixture-cloud/pack") {
		t.Fatalf("expected detection_pack_foreign_unit naming acme/b, got %v", diags)
	}
}

// Turning off one rule leaves the rest of its pack live, and the pack reports
// the disabled rule.
func TestDisabledRuleUnitLeavesItsPackLoaded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDetectionPack(t, dir, "pack-a", "acme/a", "fixture-cloud", map[string]string{
		"rules/destroy.yml": destroyRuleYAML,
		"rules/rotate.yml":  rotateRuleYAML,
	})
	content := inventoryUnitPack(t, dir, "pack-a", "acme/a")

	desired := enabledExtensionPacks("acme/a")
	desired.Disabled = []string{"host/detection-packs/acme/a:fixture-cloud/rules/rotate"}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{content},
		Desired: desired,
	})
	packs, _ := extpacks.LoadEffectiveDetectionPacks(eff)
	if len(packs) != 1 || len(packs[0].Rules) != 1 {
		t.Fatalf("want one pack with one rule, got %+v", packs)
	}
	if packs[0].Rules[0].Slug != "destroy" {
		t.Fatalf("wrong rule survived: %s", packs[0].Rules[0].Slug)
	}
	if !warnsAbout(packs[0], "rotate") {
		t.Fatalf("the pack must report the disabled rule: %v", packs[0].LoadWarnings)
	}
}

// A detection pack id is claimed once: the first claimant by provenance keeps it
// and stays live, and the later one is refused by name.
func TestLaterClaimOnADetectionPackIDIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDetectionPack(t, dir, "pack-a", "acme/a", "fixture-cloud", map[string]string{
		"rules/destroy.yml": destroyRuleYAML,
	})
	writeDetectionPack(t, dir, "pack-b", "acme/b", "fixture-cloud", map[string]string{
		"rules/rotate.yml": rotateRuleYAML,
	})
	a := inventoryUnitPack(t, dir, "pack-a", "acme/a")
	b := inventoryUnitPack(t, dir, "pack-b", "acme/b")

	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a, b},
		Desired: enabledExtensionPacks("acme/a", "acme/b"),
	})
	packs, diags := extpacks.LoadEffectiveDetectionPacks(eff)
	if len(packs) != 1 {
		t.Fatalf("want exactly the first claimant, got %+v", packs)
	}
	if packs[0].ProviderPackID != "acme/a" || len(packs[0].Rules) != 1 {
		t.Fatalf("first claimant must keep the id and its rules: %+v", packs[0])
	}
	if !hasDiagnostic(diags, extpacks.DiagDetectionPackIDTaken, "host/detection-packs/acme/b:fixture-cloud/pack") {
		t.Fatalf("expected detection_pack_id_taken naming acme/b, got %v", diags)
	}
	for _, d := range diags {
		if d.PackID == "acme/a" {
			t.Fatalf("the first claimant must carry no refusal: %+v", d)
		}
	}
}

// An installed pack claiming a shipped detection-pack id loses the claim and
// leaves the shipped rules running.
func TestInstalledPackCannotTakeAShippedDetectionPackID(t *testing.T) {
	t.Parallel()
	stock := stockPackContent(t, "painted-wolf/security")
	shippedID := firstDetectionPackID(t, stock)

	dir := t.TempDir()
	writeDetectionPack(t, dir, "squatter", "acme/squatter", shippedID, map[string]string{
		"rules/rotate.yml": rotateRuleYAML,
	})
	installed := inventoryUnitPack(t, dir, "squatter", "acme/squatter")

	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{stockPackContent(t, "painted-wolf/platform"), stock, installed},
		Desired: enabledExtensionPacks("acme/squatter"),
	})
	packs, diags := extpacks.LoadEffectiveDetectionPacks(eff)
	var claimed *detectionpack.Pack
	for i := range packs {
		if packs[i].ID == shippedID {
			claimed = &packs[i]
		}
	}
	if claimed == nil || claimed.ProviderPackID != "painted-wolf/security" {
		t.Fatalf("the shipped pack must keep %q, got %+v", shippedID, claimed)
	}
	if len(claimed.Rules) == 0 {
		t.Fatalf("the shipped rules must stay live, got %+v", claimed)
	}
	if !hasDiagnostic(diags, extpacks.DiagDetectionPackIDTaken,
		"host/detection-packs/acme/squatter:"+shippedID+"/pack") {
		t.Fatalf("expected detection_pack_id_taken for the installed pack, got %v", diags)
	}
}

// stockPackContent loads one shipped pack from the embedded catalog.
func stockPackContent(t *testing.T, id string) extpacks.PackContent {
	t.Helper()
	all, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	for _, pc := range all {
		if pc.Pack.ID == id {
			return pc
		}
	}
	t.Fatalf("stock pack %s not found", id)
	return extpacks.PackContent{}
}

// firstDetectionPackID names one detection pack the stock security pack ships.
func firstDetectionPackID(t *testing.T, stock extpacks.PackContent) string {
	t.Helper()
	best := ""
	for _, u := range stock.Units {
		id, ok := strings.CutPrefix(u.ID, extpacks.DetectionPackUnitPrefix)
		if !ok {
			continue
		}
		_, rest, ok := strings.Cut(id, ":")
		if !ok {
			continue
		}
		name, leaf, ok := strings.Cut(rest, "/")
		if !ok || leaf != "pack" {
			continue
		}
		if best == "" || name < best {
			best = name
		}
	}
	if best == "" {
		t.Fatal("stock security pack ships no detection packs")
	}
	return best
}

// Rehearsal is the authoring gate: a rule that claims a case it does not match
// is reported by name rather than discovered later as silence.
func TestRehearsalReportsAClaimThatDoesNotHold(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDetectionPack(t, dir, "pack-a", "acme/a", "fixture-cloud", map[string]string{
		"rules/destroy.yml": destroyRuleYAML,
		"fixtures.yaml": "fixtures:\n" +
			"  - rule: destroy\n" +
			"    positive:\n" +
			"      - fixturectl destroy --all\n" +
			// This one is a claim the rule does not meet: no destroy verb.
			"      - fixturectl list\n" +
			"    negative:\n" +
			"      - fixturectl status\n",
	})
	content := inventoryUnitPack(t, dir, "pack-a", "acme/a")
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{content},
		Desired: enabledExtensionPacks("acme/a"),
	})
	semantics, err := detectionpack.LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)

	diags := extpacks.RehearseDetectionPacks(eff, semantics)
	if len(diags) != 1 {
		t.Fatalf("want exactly the one failing case, got %v", diags)
	}
	if diags[0].Code != extpacks.DiagDetectionRehearsalFailed ||
		!strings.Contains(diags[0].Message, "fixturectl list") {
		t.Fatalf("diagnostic must name the case that did not hold: %+v", diags[0])
	}
	// Rehearsal failures never block the load.
	packs, _ := extpacks.LoadEffectiveDetectionPacks(eff)
	if len(packs) != 1 || len(packs[0].Rules) != 1 {
		t.Fatalf("a pack that failed rehearsal must still load: %+v", packs)
	}
}

func TestDetectionPackWithoutManifestDoesNotLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDetectionPackRulesOnly(t, dir, "pack-a", "acme/a", "fixture-cloud", map[string]string{
		"rules/destroy.yml": destroyRuleYAML,
	})
	content := inventoryUnitPack(t, dir, "pack-a", "acme/a")
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{content},
		Desired: enabledExtensionPacks("acme/a"),
	})
	packs, diags := extpacks.LoadEffectiveDetectionPacks(eff)
	if len(packs) != 0 {
		t.Fatalf("rules with no manifest loaded: %+v", packs)
	}
	if !hasDiagnostic(diags, extpacks.DiagDetectionPackInvalid, "host/detection-packs/acme/a:fixture-cloud/pack") {
		t.Fatalf("expected detection_pack_invalid, got %v", diags)
	}
}

// Installed packs sort after shipped ones, so nothing installed can take an id
// the app itself answers to.
func TestContributedPacksSortShippedFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDetectionPack(t, dir, "pack-z", "acme/z", "fixture-cloud", map[string]string{
		"rules/destroy.yml": destroyRuleYAML,
	})
	content := inventoryUnitPack(t, dir, "pack-z", "acme/z")

	stock, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	withInstalled := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   append(stock, content),
		Desired: enabledExtensionPacks("acme/z"),
	})
	packs, _ := extpacks.LoadEffectiveDetectionPacks(withInstalled)
	seenInstalled := false
	for _, p := range packs {
		switch p.Source {
		case detectionpack.SourceExtension:
			seenInstalled = true
		default:
			if seenInstalled {
				t.Fatalf("shipped pack %s sorted after an installed one", p.ID)
			}
		}
	}
	if !seenInstalled {
		t.Fatal("the installed pack did not contribute")
	}
}

func hasDiagnostic(diags []extpacks.Diagnostic, code, unitID string) bool {
	for _, d := range diags {
		if d.Code == code && d.UnitID == unitID {
			return true
		}
	}
	return false
}

func warnsAbout(p detectionpack.Pack, slug string) bool {
	for _, w := range p.LoadWarnings {
		if strings.Contains(w, slug) {
			return true
		}
	}
	return false
}

// writeDetectionPack writes an extension pack carrying one detection pack.
func writeDetectionPack(t *testing.T, root, leaf, packID, detectionID string, rules map[string]string) {
	t.Helper()
	files := map[string]string{
		detectionRel(detectionID, "pack.yaml"): fixturePackYAML,
	}
	for rel, body := range rules {
		files[detectionRel(detectionID, rel)] = body
	}
	writePackWithUnits(t, root, leaf, packID, files)
}

// writeDetectionPackRulesOnly writes rules with no manifest beside them.
func writeDetectionPackRulesOnly(t *testing.T, root, leaf, packID, detectionID string, rules map[string]string) {
	t.Helper()
	files := map[string]string{}
	for rel, body := range rules {
		files[detectionRel(detectionID, rel)] = body
	}
	writePackWithUnits(t, root, leaf, packID, files)
}

func detectionRel(detectionID, rel string) string {
	return extpacks.DetectionPackUnitPrefix + detectionID + "/" + rel
}
