package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hintregistry"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

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
