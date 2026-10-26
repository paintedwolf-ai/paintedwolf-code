package confine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestControlPlaneClassificationDoesNotChangeStorage(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	root := filepath.Join(t.TempDir(), "state")
	t.Setenv("LYCAON_CONFIG_DIR", root)
	check := func() {
		t.Helper()
		state := filepath.Join(root, "state.db")
		if !confine.ControlPlaneTree(state) || !confine.ControlPlaneReadDenied(state) {
			t.Fatal("active control-plane state was not protected")
		}
		for _, workspace := range enginepaths.AgentWorkspaceRootsUnder(root) {
			path := filepath.Join(workspace, "entry")
			if confine.ControlPlaneTree(path) || confine.ControlPlaneReadDenied(path) {
				t.Fatalf("managed workspace was denied: %s", path)
			}
		}
		scratch := enginepaths.SessionScratchUnder(root, "current")
		path := filepath.Join(scratch, "entry")
		if confine.ControlPlanePathDenied(path, true, scratch, "") || confine.ControlPlanePathDenied(path, false, scratch, "") {
			t.Fatal("current session scratch was denied")
		}
		other := enginepaths.SessionScratchUnder(root, "other")
		if !confine.ControlPlanePathDenied(other, true, scratch, "") || !confine.ControlPlanePathDenied(other, false, scratch, "") {
			t.Fatal("another session's scratch was exposed")
		}
	}
	check()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("classification created state storage: %v", err)
	}
	testutil.FailErr(t, "create config root", os.Mkdir(root, 0o755))
	testutil.FailErr(t, "set config mode", os.Chmod(root, 0o755))
	check()
	info, err := os.Stat(root)
	testutil.FailErr(t, "stat config root", err)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("classification changed config mode to %o", info.Mode().Perm())
	}
}

func TestControlPlaneClassificationRechecksSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	testutil.FailErr(t, "create first root", os.Mkdir(first, 0o700))
	testutil.FailErr(t, "create second root", os.Mkdir(second, 0o700))
	alias := filepath.Join(base, "active")
	testutil.FailErr(t, "link first root", os.Symlink(first, alias))
	t.Setenv("LYCAON_CONFIG_DIR", alias)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	if !confine.ControlPlaneTree(filepath.Join(first, "state.db")) || confine.ControlPlaneTree(filepath.Join(second, "state.db")) {
		t.Fatal("classification did not use the initial config root")
	}
	testutil.FailErr(t, "unlink first root", os.Remove(alias))
	testutil.FailErr(t, "link second root", os.Symlink(second, alias))
	if confine.ControlPlaneTree(filepath.Join(first, "state.db")) || !confine.ControlPlaneTree(filepath.Join(second, "state.db")) {
		t.Fatal("classification retained a stale config root")
	}
	if confine.ControlPlaneReadDenied(filepath.Join(first, "state.db")) || !confine.ControlPlaneReadDenied(filepath.Join(second, "state.db")) {
		t.Fatal("read floor retained a stale config root")
	}
}

// An empty read floor makes writeFilesystemRules skip the deny block entirely,
// so an unreadable HOME or config dir must refuse the profile rather than emit
// an unrestricted one. Empty HOME is how os.UserHomeDir reports "undefined".
func TestBuildProfileRefusesUnresolvableControlPlaneReadFloor(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	// Force UserConfigDir onto its HOME-derived path rather than the override.
	t.Setenv("LYCAON_CONFIG_DIR", "")
	t.Setenv("HOME", "")

	p, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{"/proj"}})
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

	p, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{"/proj"}})
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

	if p, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{"/proj"}}); err == nil {
		t.Fatalf("opt-out skipped the key-material precondition and built a profile:\n%s", p)
	}
}

// A resolvable floor still builds and still emits the deny block.
func TestBuildProfileStillBuildsWhenReadFloorResolves(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	p, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "confine.BuildSeatbeltProfile failed", err)
	if !strings.Contains(p, "(deny file-read*") {
		t.Fatalf("resolvable floor produced no read-deny block:\n%s", p)
	}
}
