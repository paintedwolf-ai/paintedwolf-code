package oar

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConformanceRunnerSpecPosture(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	out := t.TempDir()
	// These codes live in different stock packs (plan and platform); an empty
	// policy dir makes SeedFixturesFromScenarios load their effective union.
	codes := []string{
		"SPEC_POSTURE_STATE_FORBIDDEN",
		"SPEC_POSTURE_DELEGATION_FORBIDDEN",
		"DISALLOWED_AGENT",
	}
	testutil.FailErr(t, "seed", SeedFixturesFromScenarios("", out, codes))
	cr, err := NewConformanceRunner(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "runner", err)
	fxs, err := LoadFixturesDir(out)
	testutil.FailErr(t, "load fixtures", err)
	if len(fxs) < 3 {
		t.Fatalf("expected fixtures, got %d", len(fxs))
	}
	for _, fx := range fxs {
		if err := cr.RunFixture(fx); err != nil {
			t.Errorf("%s: %v", fx.Name, err)
		}
	}
}
