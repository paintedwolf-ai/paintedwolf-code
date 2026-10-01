package extpacks

import (
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestRevisionIsOpaqueSHA256(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "resolve", err)
	if !sha256Hex.MatchString(eff.Revision) {
		t.Fatalf("revision %q is not a SHA-256 hex digest", eff.Revision)
	}
}

func TestRevisionMovesWithBehaviorAffectingInputs(t *testing.T) {
	base, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "resolve base", err)

	disabled := EmptyDesired()
	disabled.Disabled = []string{GuidanceUnitID("coordinator-gate-blocked")}
	moved, err := resolveWithDesired(t.Context(), disabled, nil)
	testutil.FailErr(t, "resolve disabled", err)
	if base.Revision == moved.Revision {
		t.Fatal("disabling a unit must move the catalog revision")
	}

	again, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "resolve again", err)
	if base.Revision != again.Revision {
		t.Fatalf("equal inputs must produce equal revisions: %s vs %s", base.Revision, again.Revision)
	}
}
