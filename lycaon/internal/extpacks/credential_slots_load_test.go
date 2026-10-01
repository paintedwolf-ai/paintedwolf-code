package extpacks_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialUnitsResolveFromCapturedBytes(t *testing.T) {
	dir := t.TempDir()
	const path = "host/credential-slots/access.yaml"
	writePackWithUnits(t, dir, "a", "acme/a", map[string]string{path: "version: 1\nenv_keys: [ACCESS_VALUE]"})
	writePackWithUnits(t, dir, "b", "acme/b", map[string]string{path: "version: 1\nenv_keys: [SECOND_VALUE]"})
	a, b := inventoryUnitPack(t, dir, "a", "acme/a"), inventoryUnitPack(t, dir, "b", "acme/b")
	desired := enabledExtensionPacks("acme/a", "acme/b")
	resolve := func(packs []extpacks.PackContent, state extpacks.DesiredState) *extpacks.EffectiveCatalog {
		return extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: packs, Desired: state})
	}
	eff := resolve([]extpacks.PackContent{a, b}, desired)
	if other := resolve([]extpacks.PackContent{b, a}, desired); eff.Revision != other.Revision {
		t.Fatal("discovery order changed catalog revision")
	}
	const firstID = "host/credential-slots/acme/a:access"
	const secondID = "host/credential-slots/acme/b:access"
	if !eff.HasLoaded(firstID) || !eff.HasLoaded(secondID) {
		t.Fatal("same filename in different providers must compose")
	}
	testutil.FailErr(t, "mutate source after capture", os.WriteFile(filepath.Join(dir, "a", filepath.FromSlash(path)), []byte("version: 99"), 0o600))
	assertRecognition := func(eff *extpacks.EffectiveCatalog, want int) {
		t.Helper()
		units, diags, err := extpacks.LoadEffectiveCredentialSlots(eff)
		testutil.FailErr(t, "load captured contributions", err)
		if len(diags) != 0 {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
		ins, err := secretmint.Compile(units)
		testutil.FailErr(t, "compile recognition", err)
		if hits := ins.Inspect("external", map[string]any{"ACCESS_VALUE": "one", "SECOND_VALUE": "two", "password": "three"}); len(hits) != want {
			t.Fatalf("recognition count = %d, want %d", len(hits), want)
		}
	}
	assertRecognition(eff, 3)
	desired.Disabled = []string{firstID}
	disabled := resolve([]extpacks.PackContent{a, b}, desired)
	assertRecognition(disabled, 2)
	if disabled.Revision == eff.Revision {
		t.Fatal("unit disable did not change revision")
	}
	assertRecognition(resolve([]extpacks.PackContent{a}, desired), 1)
	assertRecognition(eff, 3)
	merged, provenance := extpacks.MergeDesired(enabledExtensionPacks("acme/a", "acme/b"), []string{firstID})
	project := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: []extpacks.PackContent{a, b}, Desired: merged, Provenance: provenance})
	assertRecognition(project, 3)
}

func TestCredentialUnitsRejectInvalidDefinitionsAndOwnership(t *testing.T) {
	_, err := extpacks.ParseManifest("fixture", []byte("manifest_version: 1\nid: acme/slots\nname: Credential slots\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n  requires_capabilities: [host.credential_slots]\n"))
	testutil.FailErr(t, "credential slot capability", err)
	dir := t.TempDir()
	writePackWithUnits(t, dir, "a", "acme/a", map[string]string{"host/credential-slots/access.yaml": "version: 1\nexcluded_keys: [password]"})
	a := inventoryUnitPack(t, dir, "a", "acme/a")
	const id = "host/credential-slots/acme/a:access"
	desired := enabledExtensionPacks("acme/a")
	desired.Own[id] = "acme/a"
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: []extpacks.PackContent{a}, Desired: desired})
	units, diags, err := extpacks.LoadEffectiveCredentialSlots(eff)
	if err == nil || units != nil || len(diags) != 1 || diags[0].Code != extpacks.DiagCredentialSlotsInvalid || diags[0].UnitID != id || diags[0].PackID != "acme/a" {
		t.Fatalf("invalid contribution not attributed: units=%v diags=%v err=%v", units, diags, err)
	}
	if _, exists := eff.Desired.Own[id]; exists {
		t.Fatal("provider-scoped recognition accepted ownership selection")
	}
	if extpacks.ProjectScope(extpacks.CredentialSlotsKindRoot) != extpacks.ScopeDeviceOnly {
		t.Fatal("recognition must be admitted at device scope")
	}
	for _, path := range []string{"host/credential-slots/access.yml", "host/credential-slots/sub/access.yaml", "host/credential-slots/_access.yaml", "host/credential-slots/a:b.yaml"} {
		if id := extpacks.UnitIDFor("acme/a", path); id != "" {
			t.Errorf("unsupported layout produced unit %s", id)
		}
	}
}
