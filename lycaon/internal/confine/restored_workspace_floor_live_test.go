package confine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// restoredWorkspace is a writable workspace below a tree the read floor hides.
type restoredWorkspace struct {
	name  string
	build func(control string) (workspace string, c confine.Confinement)
}

func restoredWorkspaces() []restoredWorkspace {
	underState := func(root func(string) string) func(control string) (string, confine.Confinement) {
		return func(control string) (string, confine.Confinement) {
			workspace := filepath.Join(root(control), "key", "job")
			return workspace, workspaceConfinement(workspace)
		}
	}
	return []restoredWorkspace{
		{"worker branch", underState(enginepaths.WorkerBranchesRootUnder)},
		{"session worktree", underState(enginepaths.SessionWorktreesRootUnder)},
		{"draft", underState(enginepaths.DraftsRootUnder)},
	}
}

// workspaceConfinement is the shape the host gives a worker: the branch is the
// only project root and is restored by name.
func workspaceConfinement(workspace string) confine.Confinement {
	return confine.Confinement{
		Roots:     liveBoundaryRoots(workspace),
		ReadRoots: []string{workspace},
		Network:   confine.NetworkDeny,
		EgressOff: confine.EgressOffOverride,
	}
}

// generatedFloorPaths lists a path for every write floor the rules generate
// inside workspace, so a floor added later is covered without touching a test.
func generatedFloorPaths(t *testing.T, c confine.Confinement, workspace string) []string {
	t.Helper()
	specs, err := confine.HardDenyWriteSpecs(c)
	testutil.FailErr(t, "HardDenyWriteSpecs", err)
	var paths []string
	for _, spec := range specs {
		switch {
		case spec.Regex != "" && spec.Subpath != "" && confine.PathAtOrUnder(spec.Subpath, workspace):
			for _, location := range protectedpath.AgentPolicyLocations() {
				if location.Name != "" && location.NameRegex() == spec.Regex {
					paths = append(paths, filepath.Join(spec.Subpath, location.Name), filepath.Join(spec.Subpath, "deep", "er", location.Name))
				}
			}
		case spec.Literal != "" && confine.PathAtOrUnder(spec.Literal, workspace):
			paths = append(paths, spec.Literal)
		case spec.Regex == "" && spec.Subpath != "" && spec.Subpath != workspace && confine.PathAtOrUnder(spec.Subpath, workspace):
			paths = append(paths, filepath.Join(spec.Subpath, "floor-probe.md"))
		}
	}
	if len(paths) < len(protectedpath.AgentPolicyLocations()) {
		t.Fatalf("rules generated %d floor paths in %s, fewer than the %d loader locations: %v", len(paths), workspace, len(protectedpath.AgentPolicyLocations()), paths)
	}
	return paths
}

// Every write floor the rules generate stays enforced by the kernel below a
// restored workspace, whether its path exists at launch or not. Restoring the
// workspace for reads and writes must never restore the floors inside it.
func TestKernelHoldsEveryWriteFloorInARestoredWorkspace(t *testing.T) {
	helper := requireLiveBoundary(t)
	for _, layout := range restoredWorkspaces() {
		t.Run(layout.name, func(t *testing.T) {
			workspace, c := restoredWorkspaceFixture(t, layout)
			floors := generatedFloorPaths(t, c, workspace)
			ordinary := []string{filepath.Join(workspace, "src", "main.go"), filepath.Join(workspace, "ordinary.txt")}
			rules := confine.BoundaryOf(&c).Filesystem
			for _, floor := range floors {
				if verdict := rules.Verdict(confine.AccessWrite, floor); verdict.Allowed {
					t.Fatalf("rules allow the generated floor %s", floor)
				}
			}

			// Absent at launch: nothing may create the path.
			for _, line := range kernelWrites(t, helper, c, append(append([]string(nil), floors...), ordinary...)) {
				path, allowed := line.path, line.allowed
				if want := rules.Verdict(confine.AccessWrite, path).Allowed; allowed != want {
					t.Errorf("create %s: kernel allowed=%v, rules allowed=%v", path, allowed, want)
				}
			}
			for _, floor := range floors {
				if _, err := os.Lstat(floor); err == nil {
					t.Errorf("%s exists on the host after the command", floor)
				}
			}
			for _, path := range ordinary {
				if _, err := os.Lstat(path); err != nil {
					t.Errorf("ordinary write %s did not land: %v", path, err)
				}
			}

			// Present at launch: nothing may change or move the file.
			for _, floor := range floors {
				writeFixtureFile(t, floor)
			}
			for _, line := range kernelMoves(t, helper, c, floors) {
				if line.allowed {
					t.Errorf("kernel let the command change %s", line.path)
				}
			}
			for _, floor := range floors {
				if raw, err := os.ReadFile(floor); err != nil || string(raw) != "fixture\n" {
					t.Errorf("%s after the command: %q, %v", floor, raw, err)
				}
			}
		})
	}
}

// restoredWorkspaceFixture places home and the control plane under a temporary
// tree so kernel probes cannot touch the real ones.
func restoredWorkspaceFixture(t *testing.T, layout restoredWorkspace) (string, confine.Confinement) {
	t.Helper()
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	control := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", control)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	workspace, c := layout.build(control)
	testutil.FailErr(t, "create workspace", os.MkdirAll(workspace, 0o700))
	return workspace, c
}

type kernelWrite struct {
	path    string
	allowed bool
}

// kernelWrites asks the kernel to create each path, making its parents the way
// a program would.
func kernelWrites(t *testing.T, helper string, c confine.Confinement, paths []string) []kernelWrite {
	t.Helper()
	return kernelScript(t, helper, c, `(mkdir -p "${p%/*}" && : >> "$p") 2>/dev/null`, paths)
}

// kernelMoves asks the kernel to change each existing path in place and to move
// it away; it answers allowed when either took effect.
func kernelMoves(t *testing.T, helper string, c confine.Confinement, paths []string) []kernelWrite {
	t.Helper()
	return kernelScript(t, helper, c, `(: > "$p") 2>/dev/null || mv -- "$p" "$p.moved" 2>/dev/null`, paths)
}

func kernelScript(t *testing.T, helper string, c confine.Confinement, attempt string, paths []string) []kernelWrite {
	t.Helper()
	const allowedMark, refusedMark = "kernel-allowed ", "kernel-refused "
	script := `for p in "$@"; do if ` + attempt + `; then echo "` + allowedMark + `$p"; else echo "` + refusedMark + `$p"; fi; done`
	cmd, cleanup, err := confine.Command(context.Background(), helper, "/bin/sh", append([]string{"-c", script, "sh"}, paths...), c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("confined script: %v\n%s", err, out)
	}
	var results []kernelWrite
	for _, line := range strings.Split(string(out), "\n") {
		if path, ok := strings.CutPrefix(line, allowedMark); ok {
			results = append(results, kernelWrite{path, true})
		} else if path, ok := strings.CutPrefix(line, refusedMark); ok {
			results = append(results, kernelWrite{path, false})
		}
	}
	if len(results) != len(paths) {
		t.Fatalf("script answered %d of %d paths:\n%s", len(results), len(paths), out)
	}
	return results
}
