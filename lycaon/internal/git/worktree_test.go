package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func initWorktreeRepo(t *testing.T) (mgr *Manager, dir string) {
	t.Helper()
	mgr = NewManager()
	dir = t.TempDir()
	ctx := context.Background()
	testutil.FailErr(t, "init", mgr.Init(ctx, dir))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644))
	_, commitErr1 := mgr.Commit(ctx, dir, GitCommitOpts{Message: "init", Paths: []string{"a.txt"}})
	testutil.FailErr(t, "commit", commitErr1)
	branch, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "branch", err)
	if branch == "" {
		t.Fatal("expected a branch after init commit")
	}
	return mgr, dir
}

func worktreeParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "session-worktrees", "proj")
	testutil.FailErr(t, "mkdir parent", os.MkdirAll(parent, 0o700))
	return parent
}

func TestAddListRemoveWorktreeRoundTrip(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base branch", err)
	wt := filepath.Join(worktreeParent(t), "sess-1")

	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, "session/test-1", base))
	info, err := os.Stat(wt)
	testutil.FailErr(t, "stat wt", err)
	if !info.IsDir() {
		t.Fatal("worktree path is not a directory")
	}
	branch, err := mgr.Branch(ctx, wt)
	testutil.FailErr(t, "wt branch", err)
	if branch != "session/test-1" {
		t.Fatalf("branch = %q want session/test-1", branch)
	}

	entries, err := mgr.ListWorktrees(ctx, dir)
	testutil.FailErr(t, "list", err)
	if len(entries) != 2 {
		t.Fatalf("entries = %d want 2: %+v", len(entries), entries)
	}
	wantWT := canonPath(t, wt)
	var found bool
	for _, e := range entries {
		if canonPath(t, e.Path) == wantWT {
			found = true
			if e.Branch != "session/test-1" {
				t.Fatalf("listed branch = %q", e.Branch)
			}
		}
	}
	if !found {
		t.Fatalf("worktree not listed: %+v", entries)
	}

	testutil.FailErr(t, "remove", mgr.RemoveWorktree(ctx, dir, wt))
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	entries, err = mgr.ListWorktrees(ctx, dir)
	testutil.FailErr(t, "list after remove", err)
	if len(entries) != 1 {
		t.Fatalf("entries after remove = %d want 1: %+v", len(entries), entries)
	}
}

func TestRepoConfigFingerprintUnchangedAcrossVerbs(t *testing.T) {
	runFrom := func(t *testing.T, useLinked bool) {
		t.Helper()
		mgr, dir := initWorktreeRepo(t)
		ctx := context.Background()
		base, err := mgr.Branch(ctx, dir)
		testutil.FailErr(t, "base", err)

		checkout := dir
		if useLinked {
			linked := filepath.Join(worktreeParent(t), "linked-base")
			testutil.FailErr(t, "add linked", mgr.AddWorktree(ctx, dir, linked, "session/linked-base", base))
			checkout = linked
		}

		before, err := mgr.RepoConfigFingerprint(ctx, checkout)
		testutil.FailErr(t, "fingerprint before", err)
		if before == "" {
			t.Fatal("empty fingerprint")
		}

		wt := filepath.Join(worktreeParent(t), "sess-fp")
		branch := "session/fp-topic"
		testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, branch, base))
		afterAdd, err := mgr.RepoConfigFingerprint(ctx, checkout)
		testutil.FailErr(t, "fingerprint after add", err)
		if afterAdd != before {
			t.Fatalf("fingerprint changed after add: %q vs %q", before, afterAdd)
		}

		testutil.FailErr(t, "write on branch", os.WriteFile(filepath.Join(wt, "b.txt"), []byte("two\n"), 0o644))
		_, commitErr2 := mgr.Commit(ctx, wt, GitCommitOpts{Message: "topic", Paths: []string{"b.txt"}})
		testutil.FailErr(t, "commit on branch", commitErr2)
		_, err = mgr.LandWorktree(ctx, WorktreeLandRequest{BaseDir: dir, BaseBranch: base, WorktreePath: wt, Branch: branch})
		testutil.FailErr(t, "land", err)
		afterMerge, err := mgr.RepoConfigFingerprint(ctx, checkout)
		testutil.FailErr(t, "fingerprint after merge", err)
		if afterMerge != before {
			t.Fatalf("fingerprint changed after merge: %q vs %q", before, afterMerge)
		}

		wt2 := filepath.Join(worktreeParent(t), "sess-fp-rm")
		branch2 := "session/fp-rm"
		testutil.FailErr(t, "add2", mgr.AddWorktree(ctx, dir, wt2, branch2, base))
		testutil.FailErr(t, "remove", mgr.RemoveWorktree(ctx, dir, wt2))
		afterRemove, err := mgr.RepoConfigFingerprint(ctx, checkout)
		testutil.FailErr(t, "fingerprint after remove", err)
		if afterRemove != before {
			t.Fatalf("fingerprint changed after remove: %q vs %q", before, afterRemove)
		}
	}

	t.Run("primary", func(t *testing.T) { runFrom(t, false) })
	t.Run("linked", func(t *testing.T) { runFrom(t, true) })
}

func TestAddWorktree_noForbiddenConfigKeys(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)

	beforeOut, code, err := gitexec.Run(ctx, dir, []string{"config", "--local", "--list"}, hermeticOpts(0))
	if err != nil && code < 0 {
		t.Fatalf("config list before: %v", err)
	}
	if code != 0 {
		t.Fatalf("config list before failed: %s", beforeOut)
	}

	wt := filepath.Join(worktreeParent(t), "sess-cfg")
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, "session/cfg", base))

	afterOut, code, err := gitexec.Run(ctx, dir, []string{"config", "--local", "--list"}, hermeticOpts(0))
	if err != nil && code < 0 {
		t.Fatalf("config list after: %v", err)
	}
	if code != 0 {
		t.Fatalf("config list after failed: %s", afterOut)
	}
	if string(afterOut) != string(beforeOut) {
		t.Fatalf("local config changed after worktree add\nbefore:\n%s\nafter:\n%s", beforeOut, afterOut)
	}
	body := string(afterOut)
	for _, key := range []string{
		"core.worktree=",
		"core.hookspath=",
		"core.longpaths=",
		"extensions.worktreeconfig=",
	} {
		if strings.Contains(strings.ToLower(body), key) {
			t.Fatalf("forbidden key %q present in local config:\n%s", key, body)
		}
	}
}

func TestAddWorktree_baseStillResolvesToItself(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)
	wt := filepath.Join(worktreeParent(t), "sess-top")
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, "session/top", base))

	out, code, err := gitexec.Run(ctx, dir, []string{"rev-parse", "--show-toplevel"}, hermeticOpts(0))
	if err != nil && code < 0 {
		t.Fatalf("rev-parse: %v", err)
	}
	if code != 0 {
		t.Fatalf("rev-parse failed: %s", out)
	}
	got := filepath.Clean(strings.TrimSpace(string(out)))
	want := filepath.Clean(dir)
	// Resolve symlinks the way git may have.
	if gotEval, err := filepath.EvalSymlinks(got); err == nil {
		got = gotEval
	}
	if wantEval, err := filepath.EvalSymlinks(want); err == nil {
		want = wantEval
	}
	if got != want {
		t.Fatalf("toplevel = %q want %q (not the worktree)", got, want)
	}
}

func TestRemoveWorktree_dirtyRefused(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)
	wt := filepath.Join(worktreeParent(t), "sess-dirty")
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, "session/dirty", base))
	testutil.FailErr(t, "dirty file", os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x\n"), 0o644))

	err = mgr.RemoveWorktree(ctx, dir, wt)
	if err == nil {
		t.Fatal("expected dirty remove to fail")
	}
	if _, statErr := os.Stat(wt); statErr != nil {
		t.Fatalf("worktree must still exist after refused remove: %v", statErr)
	}
}

func TestRemoveWorktree_branchSurvives(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)
	wt := filepath.Join(worktreeParent(t), "sess-keep")
	branch := "session/keep-branch"
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, branch, base))
	testutil.FailErr(t, "remove", mgr.RemoveWorktree(ctx, dir, wt))

	out, code, err := gitexec.Run(ctx, dir, []string{"show-ref", "--verify", "refs/heads/" + branch}, hermeticOpts(0))
	if err != nil && code < 0 {
		t.Fatalf("show-ref: %v", err)
	}
	if code != 0 {
		t.Fatalf("branch must survive remove: %s", out)
	}
}

func TestLandWorktree_success(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)
	wt := filepath.Join(worktreeParent(t), "sess-merge")
	branch := "session/merge-ok"
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, branch, base))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(wt, "feat.txt"), []byte("feat\n"), 0o644))
	_, commitErr3 := mgr.Commit(ctx, wt, GitCommitOpts{Message: "feat", Paths: []string{"feat.txt"}})
	testutil.FailErr(t, "commit", commitErr3)

	commits, err := mgr.LandWorktree(ctx, WorktreeLandRequest{BaseDir: dir, BaseBranch: base, WorktreePath: wt, Branch: branch})
	testutil.FailErr(t, "land", err)
	if commits != 1 {
		t.Fatalf("landed commits = %d, want 1", commits)
	}
	if _, err := os.Stat(filepath.Join(dir, "feat.txt")); err != nil {
		t.Fatalf("merged file missing: %v", err)
	}
	status, err := mgr.Status(ctx, dir)
	testutil.FailErr(t, "status", err)
	if status.Dirty {
		t.Fatalf("base should be clean after merge: %+v", status)
	}
	// Merge commit: HEAD should have two parents.
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-list", "--parents", "-n", "1", "HEAD"}, hermeticOpts(0))
	if err != nil && code < 0 {
		t.Fatalf("rev-list: %v", err)
	}
	if code != 0 {
		t.Fatalf("rev-list failed: %s", out)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 3 {
		t.Fatalf("expected merge commit with two parents, got %q", out)
	}
}

func TestLandWorktree_conflictAborts(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)
	wt := filepath.Join(worktreeParent(t), "sess-conflict")
	branch := "session/conflict"
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, branch, base))

	testutil.FailErr(t, "write base", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base-side\n"), 0o644))
	_, commitErr4 := mgr.Commit(ctx, dir, GitCommitOpts{Message: "base edit", Paths: []string{"a.txt"}})
	testutil.FailErr(t, "commit base", commitErr4)
	testutil.FailErr(t, "write wt", os.WriteFile(filepath.Join(wt, "a.txt"), []byte("wt-side\n"), 0o644))
	_, commitErr5 := mgr.Commit(ctx, wt, GitCommitOpts{Message: "wt edit", Paths: []string{"a.txt"}})
	testutil.FailErr(t, "commit wt", commitErr5)

	_, err = mgr.LandWorktree(ctx, WorktreeLandRequest{BaseDir: dir, BaseBranch: base, WorktreePath: wt, Branch: branch})
	var conflict *MergeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want MergeConflictError, got %v", err)
	}
	if len(conflict.Paths) == 0 {
		t.Fatal("expected conflicting paths")
	}
	found := false
	for _, p := range conflict.Paths {
		if p == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("paths = %v want a.txt", conflict.Paths)
	}
	if conflict.Code() != "GIT_MERGE_CONFLICT" {
		t.Fatalf("code = %q", conflict.Code())
	}

	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("MERGE_HEAD must be absent after abort: %v", err)
	}
	statusOut, code, err := gitexec.Run(ctx, dir, []string{"status", "--porcelain"}, hermeticOpts(0))
	if err != nil && code < 0 {
		t.Fatalf("status: %v", err)
	}
	if code != 0 {
		t.Fatalf("status failed: %s", statusOut)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Fatalf("base must be clean after abort: %q", statusOut)
	}
}

func TestAheadBehindRefs(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)
	wt := filepath.Join(worktreeParent(t), "sess-ab")
	branch := "session/ahead"
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, branch, base))

	for i, name := range []string{"c1.txt", "c2.txt"} {
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(wt, name), []byte(name+"\n"), 0o644))
		_, commitErr6 := mgr.Commit(ctx, wt, GitCommitOpts{Message: fmt.Sprintf("c%d", i+1), Paths: []string{name}})
		testutil.FailErr(t, "commit", commitErr6)
	}
	ahead, behind, err := mgr.AheadBehindRefs(ctx, dir, base, branch)
	testutil.FailErr(t, "ab", err)
	if ahead != 2 || behind != 0 {
		t.Fatalf("ahead/behind = %d/%d want 2/0", ahead, behind)
	}

	testutil.FailErr(t, "write base", os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644))
	_, commitErr7 := mgr.Commit(ctx, dir, GitCommitOpts{Message: "base move", Paths: []string{"base.txt"}})
	testutil.FailErr(t, "commit base", commitErr7)
	ahead, behind, err = mgr.AheadBehindRefs(ctx, dir, base, branch)
	testutil.FailErr(t, "ab2", err)
	if ahead != 2 || behind != 1 {
		t.Fatalf("ahead/behind = %d/%d want 2/1", ahead, behind)
	}
}

func TestValidateWorktreeRejectsMarkerWithoutRegistration(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	fake := t.TempDir()
	testutil.FailErr(t, "write fake marker", os.WriteFile(filepath.Join(fake, ".git"), []byte("gitdir: /tmp/not-a-worktree\n"), 0o600))
	if err := mgr.ValidateWorktree(t.Context(), dir, fake, "session/fake"); err == nil {
		t.Fatal("unregistered marker must not be a ready worktree")
	}
}

func TestValidateWorktreeAcceptsRegisteredCheckout(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	ctx := context.Background()
	base, err := mgr.Branch(ctx, dir)
	testutil.FailErr(t, "base", err)
	wt := filepath.Join(worktreeParent(t), "sess-ready")
	testutil.FailErr(t, "add", mgr.AddWorktree(ctx, dir, wt, "session/ready", base))

	gitPath := filepath.Join(wt, ".git")
	info, err := os.Stat(gitPath)
	testutil.FailErr(t, "stat .git", err)
	if info.IsDir() {
		t.Fatal("linked worktree .git must be a file")
	}
	testutil.FailErr(t, "validate", mgr.ValidateWorktree(ctx, dir, wt, "session/ready"))
}

func TestAgentWorkspaceRootsUnder_sessionWorktreesCarveOut(t *testing.T) {
	dir := t.TempDir()
	roots := enginepaths.AgentWorkspaceRootsUnder(dir)
	want := enginepaths.SessionWorktreesRootUnder(dir)
	found := false
	for _, r := range roots {
		if r == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing carve-out %q in %v", want, roots)
	}
}

func TestWorktreeGo_noShellOutEscape(t *testing.T) {
	pkgDir, err := os.Getwd()
	testutil.FailErr(t, "wd", err)
	path := filepath.Join(pkgDir, "worktree.go")
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read", err)
	body := string(data)
	for _, needle := range []string{`exec.Command`, `LookPath`, "--force"} {
		if strings.Contains(body, needle) {
			t.Fatalf("worktree.go must not contain %s", needle)
		}
	}
	for _, needle := range []string{"branch -d", "branch -D", "update-ref -d"} {
		if strings.Contains(body, needle) {
			t.Fatalf("worktree.go must not delete refs (%s)", needle)
		}
	}
}

func canonPath(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	testutil.FailErr(t, "abs", err)
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(eval)
	}
	return filepath.Clean(abs)
}
