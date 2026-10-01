package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestStockPacksPresent(t *testing.T) {
	t.Parallel()
	packs, err := extpacks.DiscoverStock()
	contractcheck.FailErr(t, "extpacks.DiscoverStock failed", err)
	want := map[string]bool{
		"painted-wolf/platform": true, "painted-wolf/security": true,
		"painted-wolf/plan": true, "painted-wolf/implement": true,
		"painted-wolf/options": true, "painted-wolf/bugbash": true,
		"painted-wolf/recon-pack":      true,
		"painted-wolf/security-survey": true, "painted-wolf/web-research": true,
		"painted-wolf/scan-guidance": true, "painted-wolf/browser": true,
		"painted-wolf/hitl": true, "painted-wolf/themes": true,
	}
	if len(packs) != len(want) {
		t.Fatalf("got %d packs want %d", len(packs), len(want))
	}
	for _, p := range packs {
		if !want[p.ID] {
			t.Fatalf("unexpected pack %q", p.ID)
		}
	}
}

func TestUniquePolicyUnitIDs(t *testing.T) {
	t.Parallel()
	entries, err := hintregistry.ListEffective()
	contractcheck.FailErr(t, "hintregistry.ListEffective failed", err)
	if len(entries) < 200 {
		t.Fatalf("expected full policy union, got %d", len(entries))
	}
	for _, ent := range entries {
		var probe map[string]any
		raw, err := ent.Path.Read()
		contractcheck.FailErr(t, "read "+ent.Path.String(), err)
		if err := yaml.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("%s: %v", ent.Path, err)
		}
		if _, has := probe["hint_codes"]; has {
			t.Fatalf("%s: stock policy must not use hint_codes wrapper", ent.Path)
		}
	}
}

func TestNoKicksInjectPeerDirs(t *testing.T) {
	t.Parallel()
	// Flat guidance replaces catalog kicks/ + inject/ peers — not scanner vendor trees.
	for _, leaf := range []string{
		"packs/painted-wolf/platform/guidance/kicks",
		"packs/painted-wolf/platform/guidance/inject",
		"packs/painted-wolf/hitl/guidance/kicks",
		"packs/painted-wolf/hitl/guidance/inject",
	} {
		p := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", leaf)
		if _, err := os.Stat(p); err == nil {
			t.Errorf("forbidden peer dir %s", p)
		}
	}
}
