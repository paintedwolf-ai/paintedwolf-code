package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

// sessionScratchFixture isolates the control plane under a temporary home and
// returns a confinement whose own scratch is one of two session scratch roots.
func sessionScratchFixture(t *testing.T) (c confine.Confinement, configDir, own, other string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir = filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	own = enginepaths.SessionScratchUnder(configDir, "chat-own")
	other = enginepaths.SessionScratchUnder(configDir, "chat-other")
	for _, dir := range []string{own, other} {
		testutil.FailErr(t, "create scratch", os.MkdirAll(dir, 0o700))
	}
	c = confine.Confinement{
		Roots:              []string{t.TempDir()},
		SessionScratchRoot: own,
	}
	return c, configDir, own, other
}

// A session's own scratch is its workspace in both lanes: what a command
// writes there it can read back. Every other session's scratch and the rest
// of the control plane stay behind the read floor.
func TestSessionScratchIsReadableAndWritableByItsOwnSession(t *testing.T) {
	c, configDir, own, other := sessionScratchFixture(t)
	writeFixtureFile(t, filepath.Join(configDir, "store.db"))
	boundary := confine.BoundaryOf(&c)
	if !boundary.Applied {
		t.Fatal("scratch confinement did not project an applied boundary")
	}
	allowed := confine.FloorVerdict{Allowed: true}
	controlPlaneRead := confine.FloorVerdict{Layer: confine.FloorReadControlPlane}
	controlPlaneWrite := confine.FloorVerdict{Layer: confine.FloorControlPlane}
	for _, probe := range []boundaryProbe{
		{name: "own scratch write", access: confine.AccessWrite, path: filepath.Join(own, "flow.sh"), want: allowed},
		{name: "own scratch read", access: confine.AccessRead, path: filepath.Join(own, "flow.sh"), exists: true, want: allowed},
		{name: "other session scratch write", access: confine.AccessWrite, path: filepath.Join(other, "flow.sh"), want: controlPlaneWrite},
		{name: "other session scratch read", access: confine.AccessRead, path: filepath.Join(other, "flow.sh"), exists: true, want: controlPlaneRead},
		{name: "control plane store read", access: confine.AccessRead, path: filepath.Join(configDir, "store.db"), exists: true, want: controlPlaneRead},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if probe.exists {
				writeFixtureFile(t, probe.path)
			}
			if got := boundary.Filesystem.Verdict(probe.access, probe.path); got != probe.want {
				t.Fatalf("%s %s = %+v, want %+v", probe.access, probe.path, got, probe.want)
			}
		})
	}
}

// The kernel agrees: a confined command writes its own scratch and reads the
// file back, while another session's scratch stays unreadable.
func TestSeatbeltLetsSessionReadBackItsOwnScratch(t *testing.T) {
	self := requireSeatbelt(t)
	c, _, own, other := sessionScratchFixture(t)
	ownFile := filepath.Join(own, "flow.sh")
	if code := confinedExit(t, self, c, "/bin/sh", "-c", `echo ok > "$1"`, "sh", ownFile); code != 0 {
		t.Fatalf("own scratch write exit = %d, want 0", code)
	}
	if code, out := confinedRun(t, self, c, "/bin/cat", ownFile); code != 0 || out != "ok\n" {
		t.Fatalf("own scratch read exit = %d, out = %q; want 0 and \"ok\\n\"", code, out)
	}
	otherFile := filepath.Join(other, "flow.sh")
	writeFixtureFile(t, otherFile)
	if code := confinedExit(t, self, c, "/bin/cat", otherFile); code == 0 {
		t.Fatal("other session scratch read exit = 0, want denied")
	}
}
