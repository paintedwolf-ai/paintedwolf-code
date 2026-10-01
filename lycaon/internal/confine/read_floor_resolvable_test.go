package confine_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An empty read floor makes writeFilesystemRules skip the deny block entirely,
// so an unreadable HOME or config dir must refuse the profile rather than emit
// an unrestricted one. Empty HOME is how os.UserHomeDir reports "undefined".
func TestBuildProfileRefusesUnresolvableControlPlaneReadFloor(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	// Force UserConfigDir onto its HOME-derived path rather than the override.
	t.Setenv("LYCAON_CONFIG_DIR", "")
	t.Setenv("HOME", "")

	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	if err == nil {
		t.Fatalf("unresolvable control-plane read floor built a profile instead of refusing:\n%s", p)
	}
	if !strings.Contains(err.Error(), "read floor unresolvable") {
		t.Fatalf("error = %v, want it to name the unresolvable read floor", err)
	}
}

// The config dir resolves through the override here, so only the ~/-relative
// key-material catalogue is unresolvable.
func TestBuildProfileRefusesUnresolvableKeyMaterialReadFloor(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", "")

	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	if err == nil {
		t.Fatalf("unresolvable key-material read floor built a profile instead of refusing:\n%s", p)
	}
	if !strings.Contains(err.Error(), "key-material read floor unresolvable") {
		t.Fatalf("error = %v, want it to name the key-material floor", err)
	}
}

// The opt-out drops control-plane denials only; the key-material floor
// survives it, so an unreadable home still refuses.
func TestReadDenyOptOutStillRequiresKeyMaterialFloor(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "off")
	t.Setenv("LYCAON_CONFIG_DIR", "")
	t.Setenv("HOME", "")

	if p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}}); err == nil {
		t.Fatalf("opt-out skipped the key-material precondition and built a profile:\n%s", p)
	}
}

// A resolvable floor still builds and still emits the deny block.
func TestBuildProfileStillBuildsWhenReadFloorResolves(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	if !strings.Contains(p, "(deny file-read*") {
		t.Fatalf("resolvable floor produced no read-deny block:\n%s", p)
	}
}
