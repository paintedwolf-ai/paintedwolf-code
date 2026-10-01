package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// boundaryProbe is one path the parity and layer tests ask the boundary about.
type boundaryProbe struct {
	name   string
	access confine.FilesystemAccess
	path   string
	// exists creates the file (and its parent) before the probe runs.
	exists bool
	want   confine.FloorVerdict
}

// verdictFixture builds a confinement exercising every filesystem layer, with a
// home and control plane under a temporary tree so kernel probes cannot touch
// the real ones. outside must lie beyond every default write root.
func verdictFixture(t *testing.T, outside string) (confine.Confinement, []boundaryProbe) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	control := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", control)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	project := t.TempDir()
	workerSource := t.TempDir()
	granted := filepath.Join(project, "pkg", "granted", "AGENTS.md")
	writeFixtureFile(t, granted)

	c := confine.Confinement{
		Roots:             []string{project},
		ReadDenyPaths:     []string{workerSource},
		PolicyWriteGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(granted)},
	}
	overlaySink := filepath.Join(project, settingsoverlay.DirName(), settingsoverlay.BasenameApprovals)
	allowed := confine.FloorVerdict{Allowed: true}
	probes := []boundaryProbe{
		{"project file", confine.AccessWrite, filepath.Join(project, "src", "main.go"), false, allowed},
		{"project instruction file", confine.AccessWrite, filepath.Join(project, "AGENTS.md"), true, confine.FloorVerdict{Layer: confine.FloorAgentPolicy}},
		{"nested instruction file, folded case", confine.AccessWrite, filepath.Join(project, "docs", "agents.md"), false, confine.FloorVerdict{Layer: confine.FloorAgentPolicy}},
		{"granted instruction file", confine.AccessWrite, granted, true, allowed},
		{"overlay approvals settings", confine.AccessWrite, overlaySink, false, confine.FloorVerdict{Layer: confine.FloorAgentPolicy}},
		{"project skill", confine.AccessWrite, filepath.Join(project, ".agents", "skills", "review", "SKILL.md"), false, confine.FloorVerdict{Layer: confine.FloorAgentPolicy}},
		{"instruction file outside every project", confine.AccessWrite, filepath.Join(home, "scratch", "AGENTS.md"), false, allowed},
		{"control plane file", confine.AccessWrite, filepath.Join(control, "approvals.yaml"), false, confine.FloorVerdict{Layer: confine.FloorControlPlane}},
		{"key material write", confine.AccessWrite, filepath.Join(home, ".ssh", "id_ed25519"), false, confine.FloorVerdict{Layer: confine.FloorProtected}},
		{"outside write roots", confine.AccessWrite, filepath.Join(outside, "notes.txt"), false, confine.FloorVerdict{Layer: confine.FloorOutsideWriteRoots}},
		{"raw disk device", confine.AccessWrite, "/dev/disk0", false, confine.FloorVerdict{Layer: confine.FloorControlPlane}},
		{"null device", confine.AccessWrite, "/dev/null", false, allowed},
		{"project read", confine.AccessRead, filepath.Join(project, "README.md"), true, allowed},
		{"key material read", confine.AccessRead, filepath.Join(home, ".ssh", "known_hosts"), true, confine.FloorVerdict{Layer: confine.FloorReadKeyMaterial}},
		{"control plane read", confine.AccessRead, filepath.Join(control, "settings.yaml"), true, confine.FloorVerdict{Layer: confine.FloorReadControlPlane}},
		{"per-action read exclusion", confine.AccessRead, filepath.Join(workerSource, "main.go"), true, confine.FloorVerdict{Layer: confine.FloorReadConfigured}},
		{"outside read", confine.AccessRead, filepath.Join(outside, "readable.txt"), true, allowed},
	}
	return c, probes
}

func writeFixtureFile(t *testing.T, path string) {
	t.Helper()
	testutil.FailErr(t, "create fixture parent", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write fixture file", os.WriteFile(path, []byte("fixture\n"), 0o600))
}

func TestFilesystemVerdictNamesEachFloorLayer(t *testing.T) {
	c, probes := verdictFixture(t, filepath.Join(string(filepath.Separator), "opt", "paintedwolf-verdict-probe"))
	boundary := confine.BoundaryOf(&c)
	if !boundary.Applied {
		t.Fatal("fixture confinement did not project an applied boundary")
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			if got := boundary.Filesystem.Verdict(probe.access, probe.path); got != probe.want {
				t.Fatalf("%s %s = %+v, want %+v", probe.access, probe.path, got, probe.want)
			}
		})
	}
}

// The managed browser renders untrusted pages, so it carries the same write
// floors and key-material read floor as any confined process, while its own
// cache and profile stay writable.
func TestBrowserProfileKeepsTheFloors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	control := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", control)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	project := t.TempDir()
	cache := enginepaths.BrowserCacheRootUnder(control)
	profile := filepath.Join(cache, "profiles", "profile-1")
	testutil.FailErr(t, "create browser profile", os.MkdirAll(profile, 0o700))
	writeFixtureFile(t, filepath.Join(home, ".ssh", "id_ed25519"))

	c := confine.Confinement{
		Roots:   []string{project, cache, profile},
		Browser: true, Network: confine.NetworkDeny, LoopbackConnect: true,
	}
	boundary := confine.BoundaryOf(&c)
	if !boundary.Applied {
		t.Fatal("browser confinement did not project an applied boundary")
	}
	allowed := confine.FloorVerdict{Allowed: true}
	for _, probe := range []boundaryProbe{
		{name: "profile write", access: confine.AccessWrite, path: filepath.Join(profile, "Default", "Cookies"), want: allowed},
		{name: "cache write", access: confine.AccessWrite, path: filepath.Join(cache, "chrome", "state"), want: allowed},
		{name: "project download", access: confine.AccessWrite, path: filepath.Join(project, "report.pdf"), want: allowed},
		{name: "project instruction file", access: confine.AccessWrite, path: filepath.Join(project, "AGENTS.md"), want: confine.FloorVerdict{Layer: confine.FloorAgentPolicy}},
		{name: "key material write", access: confine.AccessWrite, path: filepath.Join(home, ".ssh", "authorized_keys"), want: confine.FloorVerdict{Layer: confine.FloorProtected}},
		{name: "key material read", access: confine.AccessRead, path: filepath.Join(home, ".ssh", "id_ed25519"), want: confine.FloorVerdict{Layer: confine.FloorReadKeyMaterial}},
		{name: "control plane write", access: confine.AccessWrite, path: filepath.Join(control, "approvals.yaml"), want: confine.FloorVerdict{Layer: confine.FloorControlPlane}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := boundary.Filesystem.Verdict(probe.access, probe.path); got != probe.want {
				t.Fatalf("%s %s = %+v, want %+v", probe.access, probe.path, got, probe.want)
			}
		})
	}
}

// Only the control plane is system-terminal; every other deny names the
// capability whose declaration asks.
func TestFloorLayersDeclareTheirAskRoute(t *testing.T) {
	for _, layer := range confine.FloorLayers() {
		recovery := layer.Recovery()
		terminal := layer == confine.FloorControlPlane || layer == confine.FloorReadControlPlane
		if recovery.Terminal() != terminal {
			t.Errorf("%s terminal = %v, want %v", layer, recovery.Terminal(), terminal)
		}
		if !terminal && recovery.Grant == "" {
			t.Errorf("%s names capability %q without a grant shape", layer, recovery.Capability)
		}
	}
}
