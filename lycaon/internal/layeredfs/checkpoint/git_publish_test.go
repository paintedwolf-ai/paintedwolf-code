package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func runGitTestCmdErr(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	out, code, err := gitexec.Run(t.Context(), dir, args, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err == nil && code != 0 {
		err = fmt.Errorf("git %v exited %d: %s", args, code, out)
	}
	return strings.TrimSpace(string(out)), err
}

func runGitTestCmdInput(t *testing.T, dir, input string, args ...string) {
	t.Helper()
	out, code, err := gitexec.Run(t.Context(), dir, args, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: []byte(input)})
	if err != nil || code != 0 {
		t.Fatalf("git %v in %s failed (exit %d): %v\n%s", args, dir, code, err, out)
	}
}

// publishFixture is a destination repository and a worker repository cloned from it, whose new
// objects are exported into a checkpoint the way a view exports them.
type publishFixture struct {
	dest, worker, storeDir string
}

func newPublishFixture(t *testing.T, history int) *publishFixture {
	t.Helper()
	f := &publishFixture{dest: t.TempDir(), worker: filepath.Join(t.TempDir(), "worker"), storeDir: t.TempDir()}
	runGitTestCmd(t, f.dest, "init", "-q", "-b", "main")
	for i := range history {
		testutil.FailErr(t, "write history", os.WriteFile(filepath.Join(f.dest, "history.txt"), []byte(strconv.Itoa(i)), 0o644))
		runGitTestCmd(t, f.dest, "add", "history.txt")
		runGitTestCmd(t, f.dest, "commit", "-q", "-m", "history "+strconv.Itoa(i))
	}
	// The worker starts as a copy of the destination, with the destination's main as its base.
	testutil.FailErr(t, "copy destination", os.CopyFS(f.worker, os.DirFS(f.dest)))
	runGitTestCmd(t, f.worker, "update-ref", "refs/remotes/origin/main", "HEAD")
	return f
}

// commit adds n commits of one new file each and returns the worker's tip.
func (f *publishFixture) commit(t *testing.T, n int) string {
	t.Helper()
	for i := range n {
		name := fmt.Sprintf("worker-%d.txt", i)
		testutil.FailErr(t, "write worker file", os.WriteFile(filepath.Join(f.worker, name), []byte(name), 0o644))
		runGitTestCmd(t, f.worker, "add", name)
		runGitTestCmd(t, f.worker, "commit", "-q", "-m", name)
	}
	return runGitTestCmd(t, f.worker, "rev-parse", "HEAD")
}

// export packs what the worker added beyond the destination's main into the checkpoint.
func (f *publishFixture) export(t *testing.T, checkpointID, tip string) string {
	t.Helper()
	dir := CheckpointGitObjectsDir(f.storeDir, checkpointID, "")
	testutil.FailErr(t, "mkdir checkpoint objects", os.MkdirAll(filepath.Join(dir, "pack"), 0o700))
	runGitTestCmdInput(t, f.worker, tip+"\n^origin/main\n", "pack-objects", "--revs", "-q", filepath.Join(dir, "pack", "pack"))
	return dir
}

func (f *publishFixture) publish(t *testing.T, checkpointID, tip string) (GitPublishStats, error) {
	t.Helper()
	_, common, err := ResolveGitDirs(f.dest)
	testutil.FailErr(t, "resolve destination", err)
	return publishGitObjects(t.Context(), gitPublication{
		repoRoot: f.dest, commonGitDir: common, tips: []string{tip},
		sourceObjectDirs: []string{CheckpointGitObjectsDir(f.storeDir, checkpointID, "")},
	})
}

func (f *publishFixture) objectCount(t *testing.T, tip string) int {
	t.Helper()
	listed := runGitTestCmd(t, f.worker, "rev-list", "--objects", tip, "^origin/main")
	return len(strings.Fields(strings.ReplaceAll(listed, "\n", " ")))
}

func hasObject(t *testing.T, repo, oid string) bool {
	t.Helper()
	_, err := runGitTestCmdErr(t, repo, "cat-file", "-e", oid)
	return err == nil
}

// The pack carries the worker's new objects and nothing else, however deep the history beneath.
func TestPublicationCarriesOnlyTheWorkersNewObjectsAtAnyHistoryDepth(t *testing.T) {
	t.Parallel()
	counts := map[int]int{}
	for _, history := range []int{3, 400} {
		f := newPublishFixture(t, history)
		tip := f.commit(t, 3)
		f.export(t, "chk", tip)
		stats, err := f.publish(t, "chk", tip)
		testutil.FailErr(t, "publish", err)
		if want := f.objectCount(t, tip); stats.Objects != want {
			t.Fatalf("history %d: published %d objects, want the worker's %d", history, stats.Objects, want)
		}
		counts[history] = stats.Objects
		runGitTestCmd(t, f.dest, "rev-list", "--objects", tip)
	}
	if counts[3] != counts[400] {
		t.Fatalf("published objects followed history depth: %v", counts)
	}
}

// Unreachable loose objects and unrelated history packed beside the worker's objects stay out.
func TestPublicationLeavesUnrelatedObjectsBehind(t *testing.T) {
	t.Parallel()
	f := newPublishFixture(t, 2)
	tip := f.commit(t, 1)
	dir := f.export(t, "chk", tip)
	loose, err := WriteLooseGitObject(dir, "blob", []byte("unrelated loose object\n"))
	testutil.FailErr(t, "write loose", err)
	other := t.TempDir()
	runGitTestCmd(t, other, "init", "-q")
	testutil.FailErr(t, "write unrelated", os.WriteFile(filepath.Join(other, "u.txt"), []byte("unrelated history"), 0o644))
	runGitTestCmd(t, other, "add", "u.txt")
	runGitTestCmd(t, other, "commit", "-q", "-m", "unrelated")
	unrelated := runGitTestCmd(t, other, "rev-parse", "HEAD")
	runGitTestCmdInput(t, other, unrelated+"\n", "pack-objects", "--revs", "-q", filepath.Join(dir, "pack", "pack"))

	stats, err := f.publish(t, "chk", tip)
	testutil.FailErr(t, "publish", err)
	if want := f.objectCount(t, tip); stats.Objects != want {
		t.Fatalf("published %d objects, want %d", stats.Objects, want)
	}
	for _, oid := range []string{loose, unrelated} {
		if hasObject(t, f.dest, oid) {
			t.Fatalf("unrelated object %s reached the destination", oid)
		}
	}
}

// A destination that lost an object its history needs refuses the worker's commits, and its refs
// stay where they were.
func TestPublicationFailsTypedWhenTheBoundaryIsDamaged(t *testing.T) {
	t.Parallel()
	f := newPublishFixture(t, 2)
	tip := f.commit(t, 1)
	f.export(t, "chk", tip)
	base := runGitTestCmd(t, f.dest, "rev-parse", "HEAD")
	before := runGitTestCmd(t, f.dest, "for-each-ref")
	testutil.FailErr(t, "remove boundary commit", os.Remove(filepath.Join(f.dest, ".git", "objects", base[:2], base[2:])))

	_, err := ImportAndValidateCheckpointGitRefs(t.Context(), f.storeDir, "chk", "w",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/main": tip}}, map[string]string{"primary": f.dest})
	if !errors.Is(err, ErrCheckpointGitBoundary) {
		t.Fatalf("damaged boundary: %v", err)
	}
	if after := runGitTestCmd(t, f.dest, "for-each-ref"); after != before {
		t.Fatalf("refs moved after a refused publication:\n%s\nwant\n%s", after, before)
	}
}

// An empty destination receives every object the worker's history needs.
func TestPublicationIntoAnEmptyDestinationIsComplete(t *testing.T) {
	t.Parallel()
	worker := t.TempDir()
	runGitTestCmd(t, worker, "init", "-q", "-b", "main")
	for i := range 3 {
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(worker, "f.txt"), []byte(strconv.Itoa(i)), 0o644))
		runGitTestCmd(t, worker, "add", "f.txt")
		runGitTestCmd(t, worker, "commit", "-q", "-m", strconv.Itoa(i))
	}
	tip := runGitTestCmd(t, worker, "rev-parse", "HEAD")
	storeDir := t.TempDir()
	dir := CheckpointGitObjectsDir(storeDir, "chk", "")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "pack"), 0o700))
	runGitTestCmdInput(t, worker, tip+"\n", "pack-objects", "--revs", "-q", filepath.Join(dir, "pack", "pack"))
	dest := t.TempDir()
	runGitTestCmd(t, dest, "init", "-q")

	imported, err := ImportAndValidateCheckpointGitRefs(t.Context(), storeDir, "chk", "w",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/main": tip}}, map[string]string{"primary": dest})
	testutil.FailErr(t, "publish into empty destination", err)
	if imported["refs/lycaon/workers/w/heads/main"] != tip {
		t.Fatalf("imported = %v", imported)
	}
	runGitTestCmd(t, dest, "fsck", "--connectivity-only", "--no-dangling")
}
