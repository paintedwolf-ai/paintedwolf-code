//go:build darwin || linux

package confine_test

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

const spineProbeScriptEnv = "PW_CONFINE_SPINE_PROBE_SCRIPT"

func TestStateTreeSpineProbeProcess(t *testing.T) {
	script := os.Getenv(spineProbeScriptEnv)
	if script == "" {
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(script))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		err := runSpineProbeOp(fields[0], fields[1:])
		status := "ok"
		if err != nil {
			status = "denied:" + err.Error()
		}
		fmt.Printf("%s => %s\n", line, status)
	}
	os.Exit(0)
}

func runSpineProbeOp(op string, args []string) error {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch op {
	case "write":
		return os.WriteFile(arg(0), []byte(arg(1)), 0o600)
	case "mkdir":
		return os.Mkdir(arg(0), 0o700)
	case "rename":
		return os.Rename(arg(0), arg(1))
	case "noreplace":
		return platformNoReplaceRename(arg(0), arg(1))
	case "swap":
		return platformSwapRename(arg(0), arg(1))
	case "rmdir":
		return unix.Rmdir(arg(0))
	case "unlink":
		return unix.Unlink(arg(0))
	case "unlinkat_dir":
		return unix.Unlinkat(unix.AT_FDCWD, arg(0), unix.AT_REMOVEDIR)
	default:
		return fmt.Errorf("unknown op %s", op)
	}
}

func runSpineProbe(t *testing.T, helper, self string, c confine.Confinement, dir string, steps []struct {
	cmd    string
	wantOK bool
}) {
	t.Helper()
	lines := make([]string, len(steps))
	for i, s := range steps {
		lines[i] = s.cmd
	}
	c.Roots = liveBoundaryRoots(c.Roots...)
	cmd, cleanup, err := confine.Command(t.Context(), helper, self, []string{"-test.run=^TestStateTreeSpineProbeProcess$"}, c)
	testutil.FailErr(t, "prepare confined spine probe", err)
	defer cleanup()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), spineProbeScriptEnv+"="+strings.Join(lines, "\n"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("spine probe failed: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(line, " => "); ok {
			got[k] = v
		}
	}
	for _, s := range steps {
		res, ok := got[s.cmd]
		if !ok {
			t.Fatalf("missing result for %q in:\n%s", s.cmd, out)
		}
		if s.wantOK && res != "ok" {
			t.Errorf("%s: got %s, want ok", s.cmd, res)
		}
		if !s.wantOK && res == "ok" {
			t.Errorf("%s: succeeded inside sandbox, want denied", s.cmd)
		}
	}
}

func TestStateTreeAncestorRootsRefuseBeforeLaunch(t *testing.T) {
	helper := stateTreeHelper(t)
	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)

	for _, stateExists := range []bool{true, false} {
		stateMode := "existing-state"
		if !stateExists {
			stateMode = "missing-state"
		}
		t.Run(stateMode, func(t *testing.T) {
			for _, lane := range []string{"attached", "reviewed", "scratch", "durable"} {
				t.Run(lane, func(t *testing.T) {
					base := fspath.CanonicalPath(t.TempDir())
					parent := filepath.Join(base, "parent")
					state := filepath.Join(parent, "state")
					testutil.FailErr(t, "create parent dir", os.MkdirAll(parent, 0o700))
					var stateInode uint64
					if stateExists {
						testutil.FailErr(t, "create isolated state", os.MkdirAll(state, 0o700))
						testutil.FailErr(t, "seed state marker", os.WriteFile(filepath.Join(state, "approvals.yaml"), []byte("protected\n"), 0o600))
						st, statErr := os.Lstat(state)
						testutil.FailErr(t, "stat state", statErr)
						stateInode = st.Sys().(*syscall.Stat_t).Ino
					}
					parentBefore, err := os.Lstat(parent)
					testutil.FailErr(t, "stat parent", err)
					parentInode := parentBefore.Sys().(*syscall.Stat_t).Ino

					t.Setenv(configdir.EnvConfigDir, state)
					c := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
					switch lane {
					case "attached":
						c.Roots = []string{base}
					case "reviewed":
						c.GrantedWriteRoots = []string{base}
					case "scratch":
						c.SessionScratchRoot = base
					case "durable":
						confine.SetGrantedWriteRootsSource(func(string) []string { return []string{base} })
						t.Cleanup(func() { confine.SetGrantedWriteRootsSource(nil) })
					}

					fs := confine.BoundaryOf(&c).Filesystem
					for _, anc := range []string{base, parent} {
						if v := fs.Verdict(confine.AccessWriteUnlink, anc); v.Allowed || v.Layer != confine.FloorControlPlane {
							t.Fatalf("Verdict(AccessWriteUnlink, %q) = %+v, want FloorControlPlane", anc, v)
						}
					}

					stage := filepath.Join(base, "stage-dir")
					moved := filepath.Join(base, "moved-parent")
					sibFile := filepath.Join(parent, "sibling.txt")
					sibMoved := filepath.Join(parent, "sibling-moved.txt")
					sibSwap := filepath.Join(parent, "sibling-swap.txt")
					sibDir := filepath.Join(parent, "sibling-dir")
					sibDir2 := filepath.Join(parent, "sibling-dir-2")

					steps := []struct {
						cmd    string
						wantOK bool
					}{
						// Ancestor removal and rename/replace/swap must fail.
						{"rmdir " + parent, false},
						{"unlinkat_dir " + parent, false},
						{"rename " + parent + " " + moved, false},
						{"noreplace " + parent + " " + moved, false},
						{"mkdir " + stage, true},
						{"rename " + stage + " " + parent, false},
						{"swap " + stage + " " + parent, false},
						{"swap " + parent + " " + stage, false},
						{"rmdir " + base, false},
						{"rename " + base + " " + base + "-moved", false},
						// Positive controls: sibling files and directories under parent and base succeed.
						{"write " + sibFile + " hello", true},
						{"write " + sibSwap + " world", true},
						{"rename " + sibFile + " " + sibMoved, true},
						{"swap " + sibMoved + " " + sibSwap, true},
						{"unlink " + sibMoved, true},
						{"unlink " + sibSwap, true},
						{"mkdir " + sibDir, true},
						{"noreplace " + sibDir + " " + sibDir2, true},
						{"rmdir " + sibDir2, true},
						{"mkdir " + sibDir, true},
						{"unlinkat_dir " + sibDir, true},
						{"rmdir " + stage, true},
					}
					if stateExists {
						steps = append(steps,
							struct {
								cmd    string
								wantOK bool
							}{"rename " + state + " " + filepath.Join(parent, "state-moved"), false},
							struct {
								cmd    string
								wantOK bool
							}{"rmdir " + state, false},
							struct {
								cmd    string
								wantOK bool
							}{"write " + filepath.Join(state, "injected.txt") + " bad", false},
						)
					} else {
						steps = append(steps,
							struct {
								cmd    string
								wantOK bool
							}{"mkdir " + state, false},
						)
					}
					runSpineProbe(t, helper, self, c, base, steps)

					// Also verify external /bin/mv and /bin/rm -rf against parent.
					for _, ext := range [][]string{
						{"/bin/mv", parent, moved},
						{"/bin/rm", "-rf", parent},
					} {
						runC := c
						runC.Roots = liveBoundaryRoots(runC.Roots...)
						cmd, cleanup, launchErr := confine.Command(t.Context(), helper, ext[0], ext[1:], runC)
						testutil.FailErr(t, "prepare confined "+ext[0], launchErr)
						_, runErr := cmd.CombinedOutput()
						cleanup()
						if runErr == nil {
							t.Fatalf("%v succeeded on protected ancestor %s", ext, parent)
						}
					}

					parentAfter, err := os.Lstat(parent)
					testutil.FailErr(t, "stat parent after probes", err)
					if parentAfter.Sys().(*syscall.Stat_t).Ino != parentInode {
						t.Fatalf("parent inode changed: got %d want %d", parentAfter.Sys().(*syscall.Stat_t).Ino, parentInode)
					}
					if stateExists {
						stateAfter, err := os.Lstat(state)
						testutil.FailErr(t, "stat state after probes", err)
						if stateAfter.Sys().(*syscall.Stat_t).Ino != stateInode {
							t.Fatalf("state inode changed: got %d want %d", stateAfter.Sys().(*syscall.Stat_t).Ino, stateInode)
						}
					} else if _, err := os.Lstat(state); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("missing state was created: %v", err)
					}
				})
			}
		})
	}
}

func TestStateTreeSymlinkedAncestorSpineProtected(t *testing.T) {
	helper := stateTreeHelper(t)
	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)

	base := fspath.CanonicalPath(t.TempDir())
	realParent := filepath.Join(base, "real-parent")
	linkParent := filepath.Join(base, "link-parent")
	testutil.FailErr(t, "create real parent", os.MkdirAll(realParent, 0o700))
	testutil.FailErr(t, "create symlink parent", os.Symlink(realParent, linkParent))

	stateViaLink := filepath.Join(linkParent, "state")
	t.Setenv(configdir.EnvConfigDir, stateViaLink)

	c := confine.Confinement{
		Roots:             []string{t.TempDir()},
		GrantedWriteRoots: []string{base},
		Network:           confine.NetworkDeny,
		EgressOff:         confine.EgressOffOverride,
	}
	stage := filepath.Join(base, "stage")
	sib := filepath.Join(realParent, "sibling.txt")
	runSpineProbe(t, helper, self, c, base, []struct {
		cmd    string
		wantOK bool
	}{
		{"unlink " + linkParent, false},
		{"rename " + linkParent + " " + linkParent + "-moved", false},
		{"rmdir " + realParent, false},
		{"unlinkat_dir " + realParent, false},
		{"rename " + realParent + " " + realParent + "-moved", false},
		{"mkdir " + stage, true},
		{"swap " + stage + " " + realParent, false},
		{"rmdir " + stage, true},
		{"write " + sib + " ok", true},
		{"unlink " + sib, true},
	})
}

func TestStateTreeAncestorsExcludedFromDefaultRoots(t *testing.T) {
	for _, source := range []string{"temporary", "cache"} {
		t.Run(source, func(t *testing.T) {
			proj := t.TempDir()
			base := fspath.CanonicalPath(t.TempDir())
			state := filepath.Join(base, "state")
			insideState := filepath.Join(state, "cache")
			t.Setenv(configdir.EnvConfigDir, state)
			if source == "cache" {
				t.Setenv("XDG_CACHE_HOME", base)
			} else {
				t.Setenv("TMPDIR", base)
			}
			roots, err := confine.WriteRootsForBoundary("", []string{proj}, nil, "")
			testutil.FailErr(t, "admit roots with ancestor default", err)
			if !slices.Contains(roots, base) {
				t.Fatalf("default root containing state was dropped instead of protected by floors: %v", roots)
			}
			c := confine.Confinement{Roots: []string{proj}}
			fs := confine.BoundaryOf(&c).Filesystem
			if v := fs.Verdict(confine.AccessWrite, filepath.Join(state, "approvals.yaml")); v.Allowed || v.Layer != confine.FloorControlPlane {
				t.Fatalf("state write verdict under default root = %+v", v)
			}
			if v := fs.Verdict(confine.AccessWriteUnlink, base); v.Allowed || v.Layer != confine.FloorControlPlane {
				t.Fatalf("default root ancestor unlink verdict = %+v", v)
			}

			// A default root inside the state folder itself is dropped.
			if source == "cache" {
				t.Setenv("XDG_CACHE_HOME", insideState)
			} else {
				t.Setenv("TMPDIR", insideState)
			}
			rootsInside, err := confine.WriteRootsForBoundary("", []string{proj}, nil, "")
			testutil.FailErr(t, "admit roots excluding inside-state default", err)
			if slices.Contains(rootsInside, insideState) {
				t.Fatalf("default root inside state was admitted: %v", rootsInside)
			}
		})
	}
}

func TestOrdinaryReviewedRootStillLaunches(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv(configdir.EnvConfigDir, state)
	project, granted := t.TempDir(), t.TempDir()
	marker := filepath.Join(granted, "result")
	helper := stateTreeHelper(t)
	c := confine.Confinement{Roots: liveBoundaryRoots(project), GrantedWriteRoots: []string{granted}, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
	cmd, cleanup, err := confine.Command(t.Context(), helper, "/bin/sh", []string{"-c", "printf approved > \"$1\"", "fixture", marker}, c)
	testutil.FailErr(t, "prepare ordinary reviewed launch", err)
	defer cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary reviewed launch failed: %v: %s", err, out)
	}
	got, err := os.ReadFile(marker)
	testutil.FailErr(t, "read reviewed effect", err)
	if string(got) != "approved" {
		t.Fatalf("reviewed effect changed: %q", got)
	}
}
