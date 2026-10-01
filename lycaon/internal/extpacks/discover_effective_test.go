package extpacks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
)

func TestDiscoverEffectiveIncludesInstalledPacks(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	fixture := filepath.Join(t.TempDir(), "acme-pack")
	extpackstest.WriteMinimalPack(t, fixture, "acme/reach", 1)
	extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+fixture, "", "")

	packs, err := extpacks.DiscoverEffective(nil)
	testutil.FailErr(t, "extpacks.DiscoverEffective failed", err)
	var found *extpacks.Pack
	stock := 0
	for i, p := range packs {
		if p.ID == "acme/reach" {
			found = &packs[i]
		}
		if extpacks.IsStockPackID(p.ID) {
			stock++
		}
	}
	if found == nil {
		t.Fatalf("installed pack absent from DiscoverEffective; got %d packs", len(packs))
	}
	if found.Root != extpacks.OnDisk(fixture) {
		t.Fatalf("linked pack root = %q, want the author folder %q", found.Root, fixture)
	}
	if stock == 0 {
		t.Fatal("DiscoverEffective dropped stock packs; it must be stock ∪ installed")
	}

	dirs := extpacks.KindDirs(packs, "policy")
	var reached bool
	for _, dir := range dirs {
		if dir == extpacks.OnDisk(filepath.Join(fixture, "policy")) {
			reached = true
		}
	}
	if !reached {
		t.Fatalf("KindDirs omitted the installed pack's policy dir; got %v", dirs)
	}
}

func TestDiscoverEffectiveNilFailsClosedOnMissingLinkedPack(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	authorDir := filepath.Join(t.TempDir(), "gone-pack")
	extpackstest.WriteMinimalPack(t, authorDir, "acme/gone", 1)
	extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+authorDir, "", "")
	if err := os.RemoveAll(authorDir); err != nil {
		testutil.FailErr(t, "os.RemoveAll failed", err)
	}

	// Without an active snapshot, discovery resolves committed device state.
	extpacks.ClearActive()
	if _, err := extpacks.DiscoverEffective(nil); err == nil || !strings.Contains(err.Error(), "development source is unavailable") {
		t.Fatalf("missing locked author folder error = %v, want fail-closed refusal", err)
	}
}

// One pack wins a contested unit, and the catalog serves only that pack's bytes.
func TestContestedUnitServesOneWinnersBytes(t *testing.T) {
	eff := &extpacks.EffectiveCatalog{
		Loaded: map[string]extpacks.UnitEffective{
			"policy/CONTESTED": {
				ID: "policy/CONTESTED", WinnerPackID: "acme/winner", Content: []byte("winner body"),
			},
		},
	}
	content, packID, ok := eff.UnitContent("policy/CONTESTED")
	if !ok || packID != "acme/winner" || string(content) != "winner body" {
		t.Fatalf("winner bytes = %q from %q ok=%v", content, packID, ok)
	}
	if _, _, ok := eff.UnitContent("policy/ABSENT"); ok {
		t.Fatal("absent unit resolved content")
	}
}
