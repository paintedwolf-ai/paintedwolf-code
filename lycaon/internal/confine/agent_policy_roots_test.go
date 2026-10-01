package confine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// registeredLongRoots registers count project roots whose paths are each
// several hundred bytes, the shape that once folded into one oversized regex.
func registeredLongRoots(t *testing.T, count int) []string {
	t.Helper()
	base := t.TempDir()
	roots := make([]string, 0, count)
	for i := range count {
		root := filepath.Join(base, strings.Repeat("registered-project-", 10)+string(rune('a'+i)), "checkout")
		testutil.FailErr(t, "create registered root", os.MkdirAll(root, 0o700))
		roots = append(roots, fspath.CanonicalPath(root))
	}
	confine.SetAgentPolicyRootsSource(func() []string { return roots })
	t.Cleanup(func() { confine.SetAgentPolicyRootsSource(nil) })
	return roots
}

// packageExecutionConfinement mirrors the read floor a package execution adds:
// the catalog's protected read roots plus protected files found in the project.
func packageExecutionConfinement(t *testing.T, project string) confine.Confinement {
	t.Helper()
	catalog, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load sensitive paths", err)
	envFile := filepath.Join(project, ".env")
	writeFixtureFile(t, envFile)
	reads := catalog.ProtectedPathRoots(sensitivepath.ModeRead)
	if match, ok := catalog.Match(envFile, sensitivepath.ModeRead); ok && match.Protected {
		reads = append(reads, envFile)
	}
	return confine.Confinement{Roots: []string{project}, ReadDenyPaths: reads, Network: confine.NetworkDeny}
}

func TestAgentPolicyFloorCoversEveryRegisteredRootWithReadableFilters(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	roots := registeredLongRoots(t, 8)
	project := t.TempDir()
	c := packageExecutionConfinement(t, project)
	_, err := confine.BuildProfile(c)
	testutil.FailErr(t, "BuildProfile", err)
	boundary := confine.BoundaryOf(&c)
	if !boundary.Applied {
		t.Fatal("package-execution confinement did not project an applied boundary")
	}
	for _, root := range append(roots, fspath.CanonicalPath(project)) {
		for _, path := range []string{filepath.Join(root, "AGENTS.md"), filepath.Join(root, "pkg", "agents.MD")} {
			if got := boundary.Filesystem.Verdict(confine.AccessWrite, path); got.Allowed || got.Layer != confine.FloorAgentPolicy {
				t.Fatalf("write %s = %+v, want the agent-policy floor", path, got)
			}
		}
	}
	if got := boundary.Filesystem.Verdict(confine.AccessWrite, filepath.Join(project, "AGENTS.md.bak")); !got.Allowed {
		t.Fatalf("a non-policy project file must stay writable, got %+v", got)
	}
}

func TestBuildProfileRefusesFilterTheProfileReaderCannotRead(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	long := "/" + strings.Repeat("x", 1100)
	_, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}, ReadDenyPaths: []string{long}, Network: confine.NetworkDeny})
	if err == nil || !strings.Contains(err.Error(), "profile reader") {
		t.Fatalf("BuildProfile with a %d-byte filter = %v, want a profile reader refusal", len(long), err)
	}
}

// The kernel must accept the package-execution profile however many projects
// are registered, and enforce the agent-policy floor it carries.
func TestSeatbeltAppliesPackageExecutionProfileWithManyRegisteredRoots(t *testing.T) {
	self := requireSeatbelt(t)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	registeredLongRoots(t, 8)
	project := t.TempDir()
	c := packageExecutionConfinement(t, project)
	if code, out := confinedRun(t, self, c, "/usr/bin/true"); code != 0 {
		t.Fatalf("confined /usr/bin/true exit %d: %s", code, out)
	}
	source := filepath.Join(project, "src", "main.go")
	testutil.FailErr(t, "create source dir", os.MkdirAll(filepath.Dir(source), 0o700))
	if code := confinedExit(t, self, c, "/bin/sh", "-c", `: >> "$1"`, "sh", source); code != 0 {
		t.Fatalf("project write exit %d, want 0", code)
	}
	policy := filepath.Join(project, "src", "Agents.md")
	if code := confinedExit(t, self, c, "/bin/sh", "-c", `: >> "$1"`, "sh", policy); code == 0 {
		t.Fatalf("nested instruction file %s was writable under the agent-policy floor", policy)
	}
}
