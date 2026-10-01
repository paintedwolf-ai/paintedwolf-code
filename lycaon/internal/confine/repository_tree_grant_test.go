package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func outsideWriteGrant(path string) string {
	return confine.FloorOutsideWriteRoots.Recovery().GrantPath(path)
}

func mkdirFixture(t *testing.T, path string) {
	t.Helper()
	testutil.FailErr(t, "create fixture dir", os.MkdirAll(path, 0o700))
}

// A refused write inside a checkout names the checkout, so one approval covers
// every folder a build writes there.
func TestOutsideWriteRefusalNamesTheEnclosingWorkTree(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))

	repo := filepath.Join(home, "src", "repo")
	mkdirFixture(t, filepath.Join(repo, ".git"))
	inner := filepath.Join(repo, "vendor", "inner")
	mkdirFixture(t, filepath.Join(inner, ".git"))
	worktree := filepath.Join(home, "src", "worktree")
	writeFixtureFile(t, filepath.Join(worktree, ".git"))
	plain := filepath.Join(home, "notes")
	mkdirFixture(t, plain)

	decoy := filepath.Join(home, "decoy")
	mkdirFixture(t, filepath.Join(decoy, "sub"))
	testutil.FailErr(t, "symlink .git marker", os.Symlink(filepath.Join(repo, ".git"), filepath.Join(decoy, ".git")))

	link := filepath.Join(home, "link")
	testutil.FailErr(t, "symlink to repo", os.Symlink(repo, link))

	for _, tc := range []struct {
		name, path, want string
	}{
		{"nested build output", filepath.Join(repo, "site", "out", "index.html"), repo},
		{"direct child", filepath.Join(repo, "README.md"), repo},
		{"nearest work tree wins", filepath.Join(inner, "dist", "a.js"), inner},
		{"worktree marker file", filepath.Join(worktree, "dist", "a.js"), worktree},
		{"no work tree keeps the containing directory", filepath.Join(plain, "todo.txt"), plain},
		{"symlinked marker is not a work tree", filepath.Join(decoy, "sub", "a.txt"), filepath.Join(decoy, "sub")},
		{"symlinked component resolves to the real checkout", filepath.Join(link, "site", "index.html"), repo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := outsideWriteGrant(tc.path); got != tc.want {
				t.Fatalf("grant for %s = %s, want %s", tc.path, got, tc.want)
			}
		})
	}
}

// The home directory and top-level directories are never proposed, even when
// they hold a .git marker.
func TestOutsideWriteGrantNeverWidensToHome(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))
	mkdirFixture(t, filepath.Join(home, ".git"))
	dir := filepath.Join(home, "scratch")
	mkdirFixture(t, dir)

	if got := outsideWriteGrant(filepath.Join(dir, "a.txt")); got != dir {
		t.Fatalf("grant = %s, want %s", got, dir)
	}
}

// A broad reviewed root such as ~/.config is grantable; the control plane and
// credential stores beneath it stay refused by their floors.
func TestBroadWriteGrantKeepsInnerFloors(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".config")
	control := filepath.Join(config, "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", control)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	mkdirFixture(t, control)
	confine.SetCredentialStorePathsSource(func() []string { return []string{"~/.config/gh/"} })
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil) })
	repo := filepath.Join(home, "src", "repo")
	mkdirFixture(t, filepath.Join(repo, ".git"))

	if refused, code := confine.GrantedWriteRootRefused(config); refused {
		t.Fatalf("a reviewed ~/.config grant was refused: %s", code)
	}
	c := confine.Confinement{Roots: []string{t.TempDir()}, GrantedWriteRoots: []string{config, repo}}
	boundary := confine.BoundaryOf(&c)
	if !boundary.Applied {
		t.Fatal("fixture confinement did not project an applied boundary")
	}
	allowed := confine.FloorVerdict{Allowed: true}
	for _, probe := range []boundaryProbe{
		{name: "tool settings under the grant", access: confine.AccessWrite, path: filepath.Join(config, "tool", "settings.json"), want: allowed},
		{name: "control plane under the grant", access: confine.AccessWrite, path: filepath.Join(control, "approvals.yaml"), want: confine.FloorVerdict{Layer: confine.FloorControlPlane}},
		{name: "credential store under the grant", access: confine.AccessWrite, path: filepath.Join(config, "gh", "hosts.yml"), want: confine.FloorVerdict{Layer: confine.FloorProtected}},
		{name: "build output in a granted checkout", access: confine.AccessWrite, path: filepath.Join(repo, "site", "index.html"), want: allowed},
		// Agent policy guards the attached project's instruction files, not another checkout's.
		{name: "instruction file in a granted checkout", access: confine.AccessWrite, path: filepath.Join(repo, "AGENTS.md"), want: allowed},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := boundary.Filesystem.Verdict(probe.access, probe.path); got != probe.want {
				t.Fatalf("%s %s = %+v, want %+v", probe.access, probe.path, got, probe.want)
			}
		})
	}
}
