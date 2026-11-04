package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkerBranchCannotReadPrimarySource(t *testing.T) {
	self := requireLiveBoundary(t)
	primary := t.TempDir()
	branch := t.TempDir()
	primaryFile := filepath.Join(primary, "src", "module.py")
	branchFile := filepath.Join(branch, "src", "module.py")
	for _, file := range []string{primaryFile, branchFile} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(file), 0o700))
		testutil.FailErr(t, "write", os.WriteFile(file, []byte("value = 1\n"), 0o600))
	}

	boundary := confine.Confinement{
		Roots: liveBoundaryRoots(branch), ReadDenyPaths: []string{primary}, ReadRoots: []string{branch},
	}
	if code, out := confinedRun(t, self, boundary, "/bin/cat", branchFile); code != 0 {
		t.Fatalf("worker branch must be readable, exit=%d out=%s", code, out)
	}
	if code, out := confinedRun(t, self, boundary, "/bin/cat", primaryFile); code == 0 {
		t.Fatalf("primary source must stay read-denied, out=%s", out)
	}
}

// A worker sees its own branch and no other agent's workspace: neither a peer
// branch nor the session worktree its source came from. An approved read of one
// peer file is the only way in.
func TestWorkerCannotReadOtherWorkspaces(t *testing.T) {
	helper := requireLiveBoundary(t)
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	control := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", control)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	branches := enginepaths.WorkerBranchesRootUnder(control)
	own := filepath.Join(branches, "key", "job-a", "own.txt")
	peer := filepath.Join(branches, "key", "job-b", "peer.txt")
	otherKey := filepath.Join(branches, "other", "job-c", "peer.txt")
	source := filepath.Join(enginepaths.SessionWorktreesRootUnder(control), "session", "source.txt")
	draft := filepath.Join(enginepaths.DraftsRootUnder(control), "draft", "draft.txt")
	granted := filepath.Join(branches, "key", "job-b", "granted.txt")
	for _, file := range []string{own, peer, otherKey, source, draft, granted} {
		writeFixtureFile(t, file)
	}
	branch := filepath.Dir(own)
	c := workspaceConfinement(branch)
	c.ReadDenyPaths = append(confine.WorkerReadDenyRoots(true), filepath.Dir(source))

	rules := confine.BoundaryOf(&c).Filesystem
	expect := func(c confine.Confinement, rules confine.FilesystemRules, path string, want bool) {
		t.Helper()
		verdict := rules.Verdict(confine.AccessRead, path)
		code, out := confinedRun(t, helper, c, "/bin/cat", path)
		if verdict.Allowed != want || (code == 0) != want {
			t.Errorf("read %s: rules=%+v exit=%d want allowed=%v: %s", path, verdict, code, want, out)
		}
	}
	expect(c, rules, own, true)
	for _, path := range []string{peer, otherKey, source, draft} {
		expect(c, rules, path, false)
	}

	c.ProtectedReadGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(granted)}
	rules = confine.BoundaryOf(&c).Filesystem
	expect(c, rules, granted, true)
	expect(c, rules, peer, false)

	leg := workspaceConfinement(filepath.Dir(source))
	leg.ReadDenyPaths = confine.WorkerReadDenyRoots(false)
	rules = confine.BoundaryOf(&leg).Filesystem
	expect(leg, rules, source, true)
	expect(leg, rules, peer, false)
}
