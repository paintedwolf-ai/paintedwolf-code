package gitcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type commitFailBackend struct {
	Backend
}

func (b commitFailBackend) Commit(ctx context.Context, projectDir string, opts git.GitCommitOpts) (string, error) {
	if len(opts.Paths) > 0 {
		_, _ = b.Operate(ctx, projectDir, git.OperationRequest{Kind: "add", Paths: opts.Paths})
	}
	return "", git.NewCommitFailed([]byte("pre-commit blocked\n"), 1)
}

func TestQ18SuppressesFsmonitorAndDiffDriversWithoutMutatingConfig(t *testing.T) {
	h := newHarness(t)
	sentinelDir := t.TempDir()
	mkHelper := func(name string) string {
		sentinel := filepath.Join(sentinelDir, name)
		script := filepath.Join(sentinelDir, name+".sh")
		body := "#!/bin/sh\n: > \"" + sentinel + "\"\nexit 0\n"
		testutil.FailErr(t, "write helper", os.WriteFile(script, []byte(body), 0o755))
		return script
	}
	fsScript := mkHelper("fsmonitor")
	diffScript := mkHelper("extdiff")
	textconvScript := mkHelper("textconv")

	cfgPath := filepath.Join(h.root, ".git", "config")
	origCfg, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read config", err)
	injectedCfg := string(origCfg) +
		"\n[core]\n\tfsmonitor = " + fsScript +
		"\n[diff]\n\texternal = " + diffScript +
		"\n[diff \"payload\"]\n\ttextconv = " + textconvScript + "\n"
	testutil.FailErr(t, "write config", os.WriteFile(cfgPath, []byte(injectedCfg), 0o644))
	h.write(".gitattributes", "*.txt diff=payload\n")

	for _, argv := range [][]string{
		{"git", "status", "-s"},
		{"git", "diff"},
		{"git", "log", "-p", "-n", "1"},
		{"git", "show"},
		{"git", "blame", "a.txt"},
		{"git", "add", "new.txt"},
		{"git", "commit", "-m", "q18 commit"},
		{"git", "restore", "a.txt"},
	} {
		r := h.exec(argv...)
		if r.err != nil || r.result.Status != 0 {
			t.Fatalf("%v failed: status=%d err=%v stderr=%q", argv, r.result.Status, r.err, r.stderr)
		}
	}
	for _, name := range []string{"fsmonitor", "extdiff", "textconv"} {
		if _, err := os.Stat(filepath.Join(sentinelDir, name)); err == nil {
			t.Fatalf("helper %s executed despite hermetic suppression", name)
		}
	}
	afterCfg, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read config after", err)
	if string(afterCfg) != injectedCfg {
		t.Fatalf(".git/config was mutated:\ngot:\n%s\nwant:\n%s", afterCfg, injectedCfg)
	}
	h.exec("git", "-c", "core.fsmonitor="+fsScript, "status").refused(t, "fsmonitor -c", "COMMAND_OPTION_UNSUPPORTED")
	h.exec("git", "-c", "diff.external="+diffScript, "diff").refused(t, "diff.external -c", "COMMAND_OPTION_UNSUPPORTED")
}

func TestCommitLeavesUnrelatedStagedWorkUntouchedAndSupportsAmend(t *testing.T) {
	h := newHarness(t)
	if r := h.exec("git", "add", "new.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("git add new.txt: %+v %v", r.result, r.err)
	}
	r := h.exec("git", "commit", "-m", "add new only")
	if r.err != nil || r.result.Status != 0 || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("git commit: %+v err=%v stderr=%q", r.result, r.err, r.stderr)
	}
	if !strings.Contains(r.stdout, "add new only") {
		t.Fatalf("unexpected commit stdout %q", r.stdout)
	}
	if committed := h.git("show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(committed) != "new.txt" {
		t.Fatalf("HEAD committed %q, want only new.txt", committed)
	}
	if cached := h.git("diff", "--cached", "--name-only"); strings.TrimSpace(cached) != "staged.txt" {
		t.Fatalf("unrelated staged.txt was not preserved in index: %q", cached)
	}

	reword := h.exec("git", "commit", "--amend", "-m", "reworded subject")
	if reword.err != nil || reword.result.Status != 0 {
		t.Fatalf("amend reword: %+v %v", reword.result, reword.err)
	}
	if subj := strings.TrimSpace(h.git("log", "-1", "--format=%s")); subj != "reworded subject" {
		t.Fatalf("subject = %q, want reworded subject", subj)
	}
	if cached := h.git("diff", "--cached", "--name-only"); strings.TrimSpace(cached) != "staged.txt" {
		t.Fatalf("staged.txt lost after message-only amend: %q", cached)
	}

	noEdit := h.exec("git", "commit", "--amend", "--no-edit", "--", "a.txt")
	if noEdit.err != nil || noEdit.result.Status != 0 {
		t.Fatalf("amend --no-edit with path: %+v %v %s", noEdit.result, noEdit.err, noEdit.stderr)
	}
	if committed := h.git("show", "--name-only", "--format=", "HEAD"); !strings.Contains(committed, "a.txt") || !strings.Contains(committed, "new.txt") {
		t.Fatalf("HEAD files after path amend = %q", committed)
	}
	if cached := h.git("diff", "--cached", "--name-only"); strings.TrimSpace(cached) != "staged.txt" {
		t.Fatalf("staged.txt lost after path amend: %q", cached)
	}
}

func TestConflictAndHookFailureOutcomesReportActualEffects(t *testing.T) {
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")

	origGit := h.owners.Git
	h.owners.Git = commitFailBackend{Backend: origGit}
	h.write("hooked.txt", "hooked\n")
	headBefore := strings.TrimSpace(h.git("rev-parse", "HEAD"))
	failedCommit := h.exec("git", "commit", "-m", "should fail", "--", "hooked.txt")
	h.owners.Git = origGit
	if failedCommit.err != nil || failedCommit.result.Status == 0 || failedCommit.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("hook failure result = %+v err=%v stderr=%q", failedCommit.result, failedCommit.err, failedCommit.stderr)
	}
	if !strings.Contains(failedCommit.stderr, "pre-commit blocked") {
		t.Fatalf("missing hook stderr: %q", failedCommit.stderr)
	}
	if gotHead := strings.TrimSpace(h.git("rev-parse", "HEAD")); gotHead != headBefore {
		t.Fatalf("HEAD moved despite hook failure: %s vs %s", gotHead, headBefore)
	}
	if staged := strings.TrimSpace(h.git("diff", "--cached", "--name-only")); staged != "hooked.txt" {
		t.Fatalf("index effect not preserved: %q", staged)
	}
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")

	if r := h.exec("git", "switch", "topic"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("switch topic: %+v %v", r.result, r.err)
	}
	h.write("a.txt", "topic conflict line\n")
	if r := h.exec("git", "commit", "-a", "-m", "topic change"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("commit topic: %+v %v %s", r.result, r.err, r.stderr)
	}
	if r := h.exec("git", "switch", "main"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("switch main: %+v %v", r.result, r.err)
	}
	h.write("a.txt", "main conflict line\n")
	if r := h.exec("git", "commit", "-a", "-m", "main change"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("commit main: %+v %v", r.result, r.err)
	}

	merge := h.exec("git", "merge", "topic")
	if merge.err != nil || merge.result.Status == 0 || merge.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("conflicting merge = %+v err=%v stderr=%q", merge.result, merge.err, merge.stderr)
	}
	if _, err := os.Stat(filepath.Join(h.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD missing after conflict: %v", err)
	}
	if abort := h.exec("git", "merge", "--abort"); abort.err != nil || abort.result.Status != 0 {
		t.Fatalf("merge --abort: %+v %v", abort.result, abort.err)
	}

	if merge2 := h.exec("git", "merge", "topic"); merge2.result.Status == 0 {
		t.Fatalf("expected conflict on second merge: %+v", merge2.result)
	}
	h.write("a.txt", "resolved line\n")
	if r := h.exec("git", "add", "a.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("add resolved: %+v %v", r.result, r.err)
	}
	if cont := h.exec("git", "commit", "-m", "merged topic"); cont.err != nil || cont.result.Status != 0 {
		t.Fatalf("commit merge resolution: %+v %v %s", cont.result, cont.err, cont.stderr)
	}
}

func TestSequenceCommandsCherryPickRevertAndRebase(t *testing.T) {
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")

	if r := h.exec("git", "checkout", "-b", "seq-branch", "HEAD~1"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("checkout -b seq-branch: %+v %v", r.result, r.err)
	}
	h.write("seq.txt", "seq content\n")
	if r := h.exec("git", "add", "seq.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("add seq.txt: %+v %v", r.result, r.err)
	}
	if r := h.exec("git", "commit", "-m", "add seq"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("commit seq: %+v %v", r.result, r.err)
	}
	seqSHA := strings.TrimSpace(h.git("rev-parse", "HEAD"))

	if r := h.exec("git", "switch", "main"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("switch main: %+v %v", r.result, r.err)
	}
	if cp := h.exec("git", "cherry-pick", seqSHA); cp.err != nil || cp.result.Status != 0 {
		t.Fatalf("cherry-pick: %+v %v %s", cp.result, cp.err, cp.stderr)
	}
	if rev := h.exec("git", "revert", "--no-edit", "HEAD"); rev.err != nil || rev.result.Status != 0 {
		t.Fatalf("revert: %+v %v %s", rev.result, rev.err, rev.stderr)
	}
	if _, err := os.Stat(filepath.Join(h.root, "seq.txt")); !os.IsNotExist(err) {
		t.Fatalf("seq.txt still exists after revert: %v", err)
	}
	if r := h.exec("git", "switch", "seq-branch"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("switch seq-branch: %+v %v", r.result, r.err)
	}
	if rb := h.exec("git", "rebase", "main"); rb.err != nil || rb.result.Status != 0 {
		t.Fatalf("rebase: %+v %v %s", rb.result, rb.err, rb.stderr)
	}
}

func TestStashRetainsIDAndPopConflictKeepsStashEntry(t *testing.T) {
	h := newHarness(t)
	stashPush := h.exec("git", "stash", "push", "-u", "-m", "wip-stash")
	if stashPush.err != nil || stashPush.result.Status != 0 || stashPush.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("stash push: %+v %v %s", stashPush.result, stashPush.err, stashPush.stderr)
	}
	stashSHA := strings.TrimSpace(h.git("rev-parse", "--short", "stash@{0}"))
	if !strings.Contains(stashPush.stdout, stashSHA) {
		t.Fatalf("stash push stdout %q did not report stash SHA %q", stashPush.stdout, stashSHA)
	}
	if show := h.exec("git", "stash", "show"); show.err != nil || show.result.Status != 0 || !strings.Contains(show.stdout, "a.txt") {
		t.Fatalf("stash show: %+v %q %v", show.result, show.stdout, show.err)
	}
	h.write("a.txt", "conflicting local edit\n")
	if r := h.exec("git", "commit", "-a", "-m", "conflict base"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("commit conflict base: %+v %v", r.result, r.err)
	}
	popConflict := h.exec("git", "stash", "pop")
	if popConflict.err != nil || popConflict.result.Status == 0 || popConflict.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("stash pop conflict: %+v %v", popConflict.result, popConflict.err)
	}
	if list := strings.TrimSpace(h.git("stash", "list")); list == "" {
		t.Fatal("stash entry was dropped despite pop conflict")
	}
	if r := h.exec("git", "reset", "--hard", "HEAD~1"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("reset --hard: %+v %v", r.result, r.err)
	}
	if r := h.exec("git", "clean", "-fd"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("clean -fd: %+v %v", r.result, r.err)
	}
	popOK := h.exec("git", "stash", "pop")
	if popOK.err != nil || popOK.result.Status != 0 {
		t.Fatalf("stash pop clean: %+v %v %s", popOK.result, popOK.err, popOK.stderr)
	}
	if list := strings.TrimSpace(h.git("stash", "list")); list != "" {
		t.Fatalf("stash list not empty after successful pop: %q", list)
	}
}

func TestDestructiveReviewRecoveryAndStalePreconditionRefusal(t *testing.T) {
	h := newHarness(t)
	var reviewed []tools.FileChange
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		reviewed = append(reviewed, changes...)
		return nil
	}

	if dry := h.exec("git", "clean", "-n", "-d"); dry.err != nil || dry.result.Status != 0 || !strings.Contains(dry.stdout, "Would remove new.txt") {
		t.Fatalf("clean -n: %+v %q %v", dry.result, dry.stdout, dry.err)
	}
	if clean := h.exec("git", "clean", "-fd"); clean.err != nil || clean.result.Status != 0 {
		t.Fatalf("clean -fd: %+v %v", clean.result, clean.err)
	}
	if len(reviewed) == 0 || reviewed[len(reviewed)-1].Preview.Before != "untracked\n" {
		t.Fatalf("clean review did not capture untracked file bytes for recovery: %+v", reviewed)
	}

	if restore := h.exec("git", "restore", "a.txt"); restore.err != nil || restore.result.Status != 0 {
		t.Fatalf("restore a.txt: %+v %v", restore.result, restore.err)
	}
	if len(reviewed) == 0 || reviewed[len(reviewed)-1].Preview.Before != "one\nTWO\nthree\n" {
		t.Fatalf("restore review did not capture pre-restore bytes: %+v", reviewed)
	}

	h.write("a.txt", "before stale race\n")
	raceSeq := 0
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, c := range changes {
			if filepath.Base(c.Path) == "a.txt" {
				raceSeq++
				_ = os.WriteFile(filepath.Join(h.root, "a.txt"), []byte(fmt.Sprintf("concurrent edit during review %d\n", raceSeq)), 0o644)
			}
		}
		return nil
	}
	h.exec("git", "checkout", "--", "a.txt").refused(t, "stale checkout paths", "GIT_OPERATION_PRECONDITION")
	gotBytes, err := os.ReadFile(filepath.Join(h.root, "a.txt"))
	testutil.FailErr(t, "read a.txt", err)
	if string(gotBytes) != "concurrent edit during review 1\n" {
		t.Fatalf("concurrent edit was overwritten: %q", gotBytes)
	}
	h.exec("git", "restore", "a.txt").refused(t, "stale restore", "GIT_OPERATION_PRECONDITION")
}

func TestWorktreeAddRemovePruneAndOperandValidation(t *testing.T) {
	h := newHarness(t)
	if add := h.exec("git", "worktree", "add", "-b", "wt-topic", "./wt", "HEAD"); add.err != nil || add.result.Status != 0 {
		t.Fatalf("worktree add: %+v %v %s", add.result, add.err, add.stderr)
	}
	if list := h.exec("git", "worktree", "list"); list.err != nil || !strings.Contains(list.stdout, "wt-topic") {
		t.Fatalf("worktree list: %q %v", list.stdout, list.err)
	}
	if rm := h.exec("git", "worktree", "remove", "./wt"); rm.err != nil || rm.result.Status != 0 {
		t.Fatalf("worktree remove: %+v %v %s", rm.result, rm.err, rm.stderr)
	}
	if prune := h.exec("git", "worktree", "prune"); prune.err != nil || prune.result.Status != 0 {
		t.Fatalf("worktree prune: %+v %v %s", prune.result, prune.err, prune.stderr)
	}

	for _, argv := range [][]string{
		{"git", "add"},
		{"git", "add", "-A", "-u"},
		{"git", "commit"},
		{"git", "commit", "-a", "a.txt"},
		{"git", "restore"},
		{"git", "checkout"},
		{"git", "checkout", "-b", "b1", "--detach"},
		{"git", "switch"},
		{"git", "switch", "-c", "b1", "-d"},
		{"git", "merge"},
		{"git", "merge", "--continue", "--abort"},
		{"git", "cherry-pick"},
		{"git", "revert"},
		{"git", "rebase"},
		{"git", "stash", "unknown"},
		{"git", "reset", "--soft", "--hard"},
		{"git", "clean"},
		{"git", "clean", "-f", "-n"},
		{"git", "worktree"},
		{"git", "pull", "--ff-only", "--rebase"},
		{"git", "clone"},
		{"git", "clone", "--depth", "0", "https://example.test/r.git"},
	} {
		h.exec(argv...).refused(t, strings.Join(argv, " "), "COMMAND_OPERAND_INVALID")
	}
}

func TestSymlinkedWorkspaceRootResolvesRepoRelativeOperands(t *testing.T) {
	h := newHarness(t)
	aliasParent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve alias parent", err)
	aliasRoot := filepath.Join(aliasParent, "repo-link")
	testutil.FailErr(t, "symlink repo root", os.Symlink(h.root, aliasRoot))
	h.tc = nativefixture.Context(aliasRoot)
	h.paths, err = projectpaths.NewProgramPaths(t.Context(), h.owners.Boundary, h.tc, "")
	testutil.FailErr(t, "program paths via symlinked root", err)

	if add := h.exec("git", "add", "new.txt"); add.err != nil || add.result.Status != 0 {
		t.Fatalf("git add through symlinked root: %+v %v %s", add.result, add.err, add.stderr)
	}
	if restore := h.exec("git", "restore", "--staged", "new.txt"); restore.err != nil || restore.result.Status != 0 {
		t.Fatalf("git restore --staged through symlinked root: %+v %v %s", restore.result, restore.err, restore.stderr)
	}
}
