package extpacks

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectDisableCapabilityMatchesEveryInventoryFloor(t *testing.T) {
	for _, kind := range append(UnitKindRoots(), "unknown/future-kind") {
		for _, deviceResolved := range []bool{false, true} {
			id := kind + "/fixture"
			contributions := map[string][]UnitContribution{}
			if deviceResolved {
				contributions[id] = []UnitContribution{{PackID: "fixture/pack"}}
			}
			desired := DesiredState{Disabled: []string{id}}
			provenance := DesiredProvenance{Disabled: map[string]DesiredOrigin{id: OriginProject}}
			filtered, diagnostics := filterAdditiveDesired(desired, provenance, contributions, map[string]string{id: kind})
			allowed := ProjectDisableAllowed(kind, deviceResolved)
			if (len(filtered.Disabled) == 1) != allowed || (len(diagnostics) == 0) != allowed {
				t.Fatalf("kind=%s device_resolved=%t capability=%t filtered=%v diagnostics=%v", kind, deviceResolved, allowed, filtered.Disabled, diagnostics)
			}
		}
	}
}

func TestMergeDesiredTracksDisableProvenance(t *testing.T) {
	device := DesiredState{
		Format:   DesiredFormat,
		Disabled: []string{"policy/device", "policy/both"},
		Own:      map[string]string{"workflows/plan": "device/pack"},
	}
	merged, prov := MergeDesired(device, []string{"policy/project", "policy/both"})

	if prov.Disabled["policy/device"] != OriginDevice {
		t.Fatalf("device origin = %v", prov.Disabled["policy/device"])
	}
	if prov.Disabled["policy/project"] != OriginProject {
		t.Fatalf("project origin = %v", prov.Disabled["policy/project"])
	}
	if prov.Disabled["policy/both"] != OriginDevice {
		t.Fatalf("shared origin = %v", prov.Disabled["policy/both"])
	}
	if merged.Own["workflows/plan"] != "device/pack" {
		t.Fatalf("device own entry = %q", merged.Own["workflows/plan"])
	}
}

func TestProjectCanDisableSharedUnits(t *testing.T) {
	pack := fixtureFloorPack(t, map[string]string{
		"guidance/note.md":              "# note\n",
		"workflows/house/workflow.yaml": "id: house\nname: House\n",
	})
	desired := desiredWithExtensionPacks(pack.Pack.ID)
	merged, prov := MergeDesired(desired, []string{"guidance/note", "workflows/house"})
	eff := Resolve(t.Context(), ResolveInput{Packs: []PackContent{pack}, Desired: merged, Provenance: prov})

	for _, id := range []string{"guidance/note", "workflows/house"} {
		if eff.HasLoaded(id) {
			t.Fatalf("%s is loaded", id)
		}
		if hasDiagUnit(eff, DiagProjectScopeRefused, id) {
			t.Fatalf("%s was refused", id)
		}
	}
}

func TestProjectCannotDisableDeviceResolvedAdditiveUnit(t *testing.T) {
	pack := fixtureFloorPack(t, map[string]string{
		"policy/HOUSE.yaml": "oar: '1.0'\nid: HOUSE\nkind: policy\nanchor: tool.pre_invoke\neffect: warn\n",
	})
	desired := desiredWithExtensionPacks(pack.Pack.ID)
	merged, prov := MergeDesired(desired, []string{"policy/HOUSE"})
	eff := Resolve(t.Context(), ResolveInput{Packs: []PackContent{pack}, Desired: merged, Provenance: prov})

	if !eff.HasLoaded("policy/HOUSE") {
		t.Fatal("device policy was disabled")
	}
	if !hasDiagUnit(eff, DiagProjectScopeRefused, "policy/HOUSE") {
		t.Fatalf("missing refusal: %#v", eff.Diagnostics)
	}
}

func TestDeviceDisabledPackStaysDisabled(t *testing.T) {
	pack := fixtureFloorPack(t, map[string]string{
		"tools/profiles/x.yaml": "profiles:\n  x: true\n",
	})
	disabled := false
	desired := DesiredState{
		Format: DesiredFormat,
		Packs:  []DesiredPack{{ID: pack.Pack.ID, Enabled: &disabled}},
		Own:    map[string]string{},
	}
	merged, prov := MergeDesired(desired, nil)
	eff := Resolve(t.Context(), ResolveInput{Packs: []PackContent{pack}, Desired: merged, Provenance: prov})

	if eff.HasLoaded("tools/profiles/x") {
		t.Fatal("disabled pack contributed a unit")
	}
	if len(diagCodesFor(eff, DiagPackDisabled)) == 0 {
		t.Fatalf("missing pack diagnostic: %#v", eff.Diagnostics)
	}
}

func TestProjectFloorIsOrderInvariant(t *testing.T) {
	stock, err := DiscoverStockContent()
	testutil.FailErr(t, "discover stock", err)
	pack := fixtureFloorPack(t, map[string]string{"guidance/house.md": "# house\n"})
	desired := desiredWithExtensionPacks(pack.Pack.ID)
	merged, prov := MergeDesired(desired, []string{"guidance/house"})
	packs := append(append([]PackContent{}, stock...), pack)
	reversed := append([]PackContent{}, packs...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}

	a := Resolve(t.Context(), ResolveInput{Packs: packs, Desired: merged, Provenance: prov})
	b := Resolve(t.Context(), ResolveInput{Packs: reversed, Desired: merged, Provenance: prov})
	if a.Revision != b.Revision {
		t.Fatalf("revisions differ: %s vs %s", a.Revision, b.Revision)
	}
}

func TestProjectFloorDiagnosticCodesRegistered(t *testing.T) {
	all := AllDiagnosticCodes()
	for _, want := range []string{DiagProjectScopeRefused, DiagProjectPackSuggested} {
		found := false
		for _, code := range all {
			found = found || code == want
		}
		if !found {
			t.Fatalf("%s is not registered", want)
		}
	}
}

func fixtureFloorPack(t *testing.T, files map[string]string) PackContent {
	t.Helper()
	dir := t.TempDir()
	writeFixturePack(t, dir, "pack", Manifest{
		ID: "acme/floor", Name: "Floor",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, files)
	return mustInventoryPack(t, filepath.Join(dir, "pack"), "acme/floor")
}

func mustInventoryPack(t *testing.T, root, id string) PackContent {
	t.Helper()
	man, err := LoadManifest(root)
	if err != nil {
		man = Manifest{ID: id, Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"}}
	}
	pack, err := InventoryPack(Pack{ID: id, Root: OnDisk(root)}, man)
	testutil.FailErr(t, "inventory pack", err)
	pack.Kind = PackKindPath
	return pack
}

func diagCodesFor(eff *EffectiveCatalog, code string) []Diagnostic {
	var out []Diagnostic
	for _, diagnostic := range eff.Diagnostics {
		if diagnostic.Code == code {
			out = append(out, diagnostic)
		}
	}
	return out
}

func hasDiagUnit(eff *EffectiveCatalog, code, unitID string) bool {
	for _, diagnostic := range eff.Diagnostics {
		if diagnostic.Code == code && diagnostic.UnitID == unitID {
			return true
		}
	}
	return false
}
