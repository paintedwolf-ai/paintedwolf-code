package rules_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMaterializeGateRulesEmbedOnly(t *testing.T) {
	gates, err := rules.LoadOpengrepGates()
	testutil.FailErr(t, "LoadOpengrepGates", err)

	// Empty, relative, and non-module roots spill the embedded rules.
	for _, root := range []string{"", "lycaon", filepath.Join(t.TempDir(), "not-a-module")} {
		paths, err := rules.MaterializeGateRules(gates, root, t.TempDir())
		testutil.FailErr(t, "MaterializeGateRules("+root+")", err)
		if len(paths) == 0 {
			t.Fatalf("MaterializeGateRules(%q) returned no paths", root)
		}
		for _, p := range paths {
			if !filepath.IsAbs(p) {
				t.Fatalf("expected absolute path, got %q", p)
			}
		}
	}
}
