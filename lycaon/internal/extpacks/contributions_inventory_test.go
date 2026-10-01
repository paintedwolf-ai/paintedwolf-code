package extpacks

import (
	"path/filepath"
	"strings"
	"testing"
)

func contributionFixtureFiles(provider string) map[string]string {
	return map[string]string{
		"contributions/commands/explain-selection.yaml": "id: " + provider + ":explain-selection\ntitle: Explain\n",
		"contributions/menus/editor-context.yaml":       "id: " + provider + ":editor-context\nslot: editor.context.analysis\n",
		"contributions/keybindings/explain-binding.yaml": "id: " + provider + ":explain-binding\ncommand: " +
			provider + ":explain-selection\n",
		"contributions/editor-actions/explain.yaml":     "id: " + provider + ":explain\ntitle: Explain\n",
		"contributions/configuration/review-depth.yaml": "id: " + provider + ":review-depth\ntype: string\n",
		"contributions/mcp-requirements/github.yaml":    "id: " + provider + ":github\nprovider_id: github\n",
		"contributions/search-sources/issues.yaml":      "id: " + provider + ":issues\nlabel: Issues\nprefix: issues\n",
		"contributions/operations/create.yaml":          "id: " + provider + ":create\naction: {kind: operation, ref: " + provider + ":base}\n",
	}
}

// Contribution roots use the standard unit resolver.
func TestContributionRootsRideResolverAlgebra(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "acme-reviewer", Manifest{
		ID: "acme/reviewer", Name: "Reviewer",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, contributionFixtureFiles("acme/reviewer"))
	pack := mustInventoryPack(t, filepath.Join(dir, "acme-reviewer"), "acme/reviewer")

	desired := desiredWithExtensionPacks("acme/reviewer")
	desired.Disabled = []string{"contributions/menus/acme/reviewer:editor-context"}
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{mustStockPack(t, "painted-wolf/platform"), pack},
		Desired: desired,
	})

	for _, id := range []string{
		"contributions/commands/acme/reviewer:explain-selection",
		"contributions/keybindings/acme/reviewer:explain-binding",
		"contributions/editor-actions/acme/reviewer:explain",
		"contributions/configuration/acme/reviewer:review-depth",
		"contributions/mcp-requirements/acme/reviewer:github",
		"contributions/search-sources/acme/reviewer:issues",
		"contributions/operations/acme/reviewer:create",
	} {
		u, ok := eff.Loaded[id]
		if !ok {
			t.Fatalf("expected %s loaded", id)
		}
		if u.WinnerPackID != "acme/reviewer" {
			t.Fatalf("%s winner = %q", id, u.WinnerPackID)
		}
		wantKind, _, found := strings.Cut(id, "/acme/reviewer:")
		if !found {
			t.Fatalf("%s is not provider-scoped", id)
		}
		if u.Kind != wantKind {
			t.Fatalf("%s kind = %q want %q", id, u.Kind, wantKind)
		}
	}
	menu, ok := eff.Units["contributions/menus/acme/reviewer:editor-context"]
	if !ok || menu.Status != UnitStatusDisabled {
		t.Fatalf("disabled menu unit = %+v ok=%v", menu, ok)
	}
}

// Two packs may both call a file shared.yaml. Each declares its own namespaced
// id, so both load and neither is a bid on the other.
func TestSameFileStemInTwoPacksIsNotACollision(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "one", Manifest{
		ID: "acme/one", Name: "One", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{"contributions/commands/shared.yaml": "id: acme/one:shared\n"})
	writeFixturePack(t, dir, "two", Manifest{
		ID: "acme/two", Name: "Two", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{"contributions/commands/shared.yaml": "id: acme/two:shared\n"})
	one := mustInventoryPack(t, filepath.Join(dir, "one"), "acme/one")
	two := mustInventoryPack(t, filepath.Join(dir, "two"), "acme/two")
	packs := []PackContent{mustStockPack(t, "painted-wolf/platform"), one, two}

	eff := Resolve(t.Context(), ResolveInput{
		Packs:   packs,
		Desired: desiredWithExtensionPacks("acme/one", "acme/two"),
	})
	for packID, unitID := range map[string]string{
		"acme/one": "contributions/commands/acme/one:shared",
		"acme/two": "contributions/commands/acme/two:shared",
	} {
		u, ok := eff.Loaded[unitID]
		if !ok || u.Status != UnitStatusLoaded || u.WinnerPackID != packID {
			t.Fatalf("%s = %+v ok=%v; both packs must load their own declaration", unitID, u, ok)
		}
	}
	for _, d := range eff.Diagnostics {
		if d.Code == DiagConflict {
			t.Fatalf("contributions must not contend for a unit id: %+v", d)
		}
	}
}

// One contribution per contribution unit id, so `own:` has nothing to choose
// between and says so instead of recording a dead line.
func TestOwnRefusedForContributionUnits(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "one", Manifest{
		ID: "acme/one", Name: "One", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{"contributions/commands/shared.yaml": "id: acme/one:shared\n"})
	one := mustInventoryPack(t, filepath.Join(dir, "one"), "acme/one")

	desired := desiredWithExtensionPacks("acme/one")
	desired.Own["contributions/commands/acme/one:shared"] = "acme/one"
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{mustStockPack(t, "painted-wolf/platform"), one},
		Desired: desired,
	})
	found := false
	for _, d := range eff.Diagnostics {
		if d.Code == DiagOwnRefused && d.UnitID == "contributions/commands/acme/one:shared" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected own_refused for a contribution unit, got %+v", eff.Diagnostics)
	}
}

func TestContributionKindsAreDeviceOnly(t *testing.T) {
	for _, root := range ContributionKindRoots() {
		if ProjectScope(root) != ScopeDeviceOnly {
			t.Fatalf("%s project scope = %v want ScopeDeviceOnly", root, ProjectScope(root))
		}
	}
}
