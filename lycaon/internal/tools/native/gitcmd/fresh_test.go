package gitcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandGitFirstCommitMatchesGit(t *testing.T) {
	h := newEmptyHarness(t)
	reference := newEmptyHarness(t)
	h.write("a", "a\n")
	reference.write("a", "a\n")
	if add := h.exec("git", "add", "a"); add.err != nil || add.result.Status != 0 {
		t.Fatalf("add in unborn repository: %+v err=%v stderr=%q", add.result, add.err, add.stderr)
	}
	if commit := h.exec("git", "commit", "-q", "-m", "first"); commit.err != nil || commit.result.Status != 0 {
		t.Fatalf("first commit: %+v err=%v stderr=%q", commit.result, commit.err, commit.stderr)
	}
	if got := h.git("show", "--format=", "--name-only", "HEAD"); got != "a\n" {
		t.Fatalf("first commit files = %q", got)
	}
	if got := h.git("rev-list", "--count", "HEAD"); got != "1\n" {
		t.Fatalf("first commit count = %q", got)
	}
	for _, command := range [][]string{{"add", "a"}, {"commit", "-q", "-m", "first"}} {
		if _, stderr, code := reference.direct(reference.root, command...); code != 0 {
			t.Fatalf("reference git %v: %s", command, stderr)
		}
	}
	if got, want := h.git("ls-tree", "-r", "HEAD"), reference.git("ls-tree", "-r", "HEAD"); got != want {
		t.Fatalf("first commit tree = %q, reference = %q", got, want)
	}
	if got, want := h.git("status", "--porcelain=v1"), reference.git("status", "--porcelain=v1"); got != want {
		t.Fatalf("first commit status = %q, reference = %q", got, want)
	}
}

func TestCommandGitNamedAddIgnoresNestedRepository(t *testing.T) {
	native := newEmptyHarness(t)
	reference := newEmptyHarness(t)
	for _, h := range []*harness{native, reference} {
		addNestedFixture(t, h, false)
	}
	got := native.exec("git", "add", "README.txt")
	if got.err != nil || got.result.Status != 0 {
		t.Fatalf("named add: %+v err=%v stderr=%q", got.result, got.err, got.stderr)
	}
	if _, stderr, code := reference.direct(reference.root, "add", "README.txt"); code != 0 {
		t.Fatalf("reference named add: %s", stderr)
	}
	if got, want := native.git("status", "--porcelain=v1"), reference.git("status", "--porcelain=v1"); got != want {
		t.Fatalf("named add status = %q, reference = %q", got, want)
	}
	if got, want := native.git("ls-files", "--stage"), reference.git("ls-files", "--stage"); got != want {
		t.Fatalf("named add index = %q, reference = %q", got, want)
	}
	if got := native.git("status", "--porcelain=v1"); !strings.Contains(got, "?? gt/") {
		t.Fatalf("nested repository missing from status: %q", got)
	}
}

func TestCommandGitDirectoryAddPreservesUnchangedTrackedFiles(t *testing.T) {
	native := newEmptyHarness(t)
	reference := newEmptyHarness(t)
	for _, h := range []*harness{native, reference} {
		h.write("dir/changed.txt", "original\n")
		h.write("dir/untouched.txt", "untouched\n")
		h.write("elsewhere.txt", "other\n")
		h.git("add", ".")
		h.git("commit", "-q", "-m", "baseline")
		h.write("dir/changed.txt", "changed\n")
	}
	got := native.exec("git", "add", "dir")
	if got.err != nil || got.result.Status != 0 {
		t.Fatalf("directory add: %+v err=%v stderr=%q", got.result, got.err, got.stderr)
	}
	if _, stderr, code := reference.direct(reference.root, "add", "dir"); code != 0 {
		t.Fatalf("reference directory add: %s", stderr)
	}
	if got, want := native.git("status", "--porcelain=v1"), reference.git("status", "--porcelain=v1"); got != want {
		t.Fatalf("directory add status = %q, reference = %q", got, want)
	}
	if got, want := native.git("ls-files", "--stage"), reference.git("ls-files", "--stage"); got != want {
		t.Fatalf("directory add index = %q, reference = %q", got, want)
	}
}

func TestCommandGitAllAddsNestedRepositoryLikeGit(t *testing.T) {
	for _, args := range [][]string{{"-A"}, {"."}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			native := newEmptyHarness(t)
			reference := newEmptyHarness(t)
			for _, h := range []*harness{native, reference} {
				addNestedFixture(t, h, true)
			}
			got := native.exec(append([]string{"git", "add"}, args...)...)
			if got.err != nil || got.result.Status != 0 {
				t.Fatalf("add %v: %+v err=%v stderr=%q", args, got.result, got.err, got.stderr)
			}
			_, referenceStderr, code := reference.direct(reference.root, append([]string{"add"}, args...)...)
			if code != 0 {
				t.Fatalf("reference add %v: %s", args, referenceStderr)
			}
			if got.stderr != referenceStderr {
				t.Fatalf("add %v stderr = %q, reference = %q", args, got.stderr, referenceStderr)
			}
			if got, want := native.git("ls-files", "--stage"), reference.git("ls-files", "--stage"); got != want {
				t.Fatalf("add %v index = %q, reference = %q", args, got, want)
			}
			if got, want := native.git("status", "--porcelain=v1"), reference.git("status", "--porcelain=v1"); got != want {
				t.Fatalf("add %v status = %q, reference = %q", args, got, want)
			}
		})
	}
}

func TestCommandGitAllWithUnbornNestedRepoMatchesGitFailure(t *testing.T) {
	native := newEmptyHarness(t)
	reference := newEmptyHarness(t)
	for _, h := range []*harness{native, reference} {
		addNestedFixture(t, h, false)
	}
	got := native.exec("git", "add", "-A")
	_, referenceStderr, referenceStatus := reference.direct(reference.root, "add", "-A")
	if got.err != nil || got.result.Status != referenceStatus || got.stderr != referenceStderr {
		t.Fatalf("add -A = status %d stderr %q err %v; reference = status %d stderr %q", got.result.Status, got.stderr, got.err, referenceStatus, referenceStderr)
	}
	if got, want := native.git("status", "--porcelain=v1"), reference.git("status", "--porcelain=v1"); got != want {
		t.Fatalf("failed add status = %q, reference = %q", got, want)
	}
}

func addNestedFixture(t *testing.T, h *harness, committed bool) {
	t.Helper()
	h.write("README.txt", "readme\n")
	nested := filepath.Join(h.root, "gt")
	testutil.FailErr(t, "create nested directory", os.MkdirAll(nested, 0o755))
	if _, stderr, code := h.direct(nested, "init", "-q"); code != 0 {
		t.Fatalf("init nested repository: %s", stderr)
	}
	testutil.FailErr(t, "write nested file", os.WriteFile(filepath.Join(nested, "inner"), []byte("inner\n"), 0o644))
	if !committed {
		return
	}
	for _, command := range [][]string{{"add", "inner"}, {"commit", "-q", "-m", "inner"}} {
		if _, stderr, code := h.direct(nested, command...); code != 0 {
			t.Fatalf("nested git %v: %s", command, stderr)
		}
	}
}
