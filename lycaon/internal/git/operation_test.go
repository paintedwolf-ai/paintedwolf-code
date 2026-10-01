//go:build integration

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func operationFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Init(t, dir)
	gittest.Run(t, dir, "config", "user.name", "Test")
	gittest.Run(t, dir, "config", "user.email", "test@example.com")
	operationWrite(t, dir, "shared.txt", "base\n")
	operationWrite(t, dir, "unrelated.txt", "original\n")
	gittest.Run(t, dir, "add", "--", "shared.txt", "unrelated.txt")
	gittest.Run(t, dir, "commit", "-m", "Initial")
	gittest.Run(t, dir, "branch", "-M", "main")
	return dir
}
func operationWrite(t *testing.T, dir, path, body string) {
	t.Helper()
	testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755))
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644))
}
func operateOK(t *testing.T, dir string, req OperationRequest) OperationResult {
	t.Helper()
	result, err := NewManager().Operate(t.Context(), dir, req)
	testutil.FailErr(t, "operate", err)
	if result.Status != "completed" && result.Status != "no_op" {
		t.Fatalf("operation failed: %+v", result)
	}
	return result
}

func TestOperationBranchSynchronization(t *testing.T) {
	dir := operationFixture(t)
	gittest.Run(t, dir, "checkout", "-b", "benchmark-page")
	operationWrite(t, dir, "benchmark.txt", "benchmark\n")
	gittest.Run(t, dir, "add", "--", "benchmark.txt")
	gittest.Run(t, dir, "commit", "-m", "Benchmark")
	gittest.Run(t, dir, "checkout", "main")
	gittest.Run(t, dir, "checkout", "-b", "fix/example")
	operationWrite(t, dir, "shared.txt", "updated\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	gittest.Run(t, dir, "commit", "-m", "Fix")
	reviewed := false
	operateOK(t, dir, OperationRequest{Kind: "checkout", Branch: "main", Review: func(_ context.Context, files []RestoreFile) error {
		reviewed = true
		if len(files) != 2 || string(files[0].After.Bytes) != "base\n" {
			t.Fatalf("incorrect preview: %+v", files)
		}
		return nil
	}})
	if !reviewed {
		t.Fatal("checkout skipped review")
	}
	operateOK(t, dir, OperationRequest{Kind: "merge", Ref: "fix/example", Mode: "ff_only"})
	operateOK(t, dir, OperationRequest{Kind: "checkout", Branch: "benchmark-page"})
	gittest.Run(t, dir, "config", "branch.benchmark-page.mergeOptions", "--squash")
	merged := operateOK(t, dir, OperationRequest{Kind: "merge", Ref: "main"})
	if merged.After.Branch != "benchmark-page" || merged.After.MergeHead != "" {
		t.Fatalf("bad merge state: %+v", merged)
	}
	comparison, err := NewManager().Compare(t.Context(), dir, "main", "benchmark-page")
	testutil.FailErr(t, "compare", err)
	if comparison.Behind != 0 || comparison.Ahead != 2 {
		t.Fatalf("comparison: %+v", comparison)
	}
	operateOK(t, dir, OperationRequest{Kind: "checkout", Branch: "main"})
}

func TestOperationReviewRefusalAndStaleChanges(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(map[bool]string{false: "refusal", true: "stale"}[mutate], func(t *testing.T) {
			dir := operationFixture(t)
			gittest.Run(t, dir, "checkout", "-b", "feature")
			operationWrite(t, dir, "shared.txt", "feature\n")
			gittest.Run(t, dir, "add", "--", "shared.txt")
			gittest.Run(t, dir, "commit", "-m", "Feature")
			refusal := errors.New("review refused")
			_, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "checkout", Branch: "main", Review: func(context.Context, []RestoreFile) error {
				if mutate {
					operationWrite(t, dir, "shared.txt", "concurrent edit\n")
					return nil
				}
				return refusal
			}})
			if mutate {
				var pre *OperationError
				if !errors.As(err, &pre) || pre.Reason != "repository_changed_during_review" {
					t.Fatalf("stale review not refused: %v", err)
				}
			} else if !errors.Is(err, refusal) {
				t.Fatalf("refusal lost: %v", err)
			}
			if branch := gittest.Run(t, dir, "branch", "--show-current"); branch != "feature\n" {
				t.Fatalf("changed branch before approval: %q", branch)
			}
		})
	}
}

func TestOperationScopedStashPreservesOtherIndexEntries(t *testing.T) {
	dir := operationFixture(t)
	operationWrite(t, dir, "shared.txt", "saved\n")
	operationWrite(t, dir, "unrelated.txt", "someone else's staged work\n")
	operationWrite(t, dir, "new file.txt", "new\n")
	gittest.Run(t, dir, "add", "--", "shared.txt", "unrelated.txt")
	staged := gittest.Run(t, dir, "show", ":unrelated.txt")
	saved := operateOK(t, dir, OperationRequest{Kind: "stash", Action: "save", Paths: []string{"shared.txt", "new file.txt"}, IncludeUntracked: true, Message: "Selected work"})
	if saved.After.Stash == "" {
		t.Fatal("no stash identity returned")
	}
	if got := gittest.Run(t, dir, "show", ":unrelated.txt"); got != staged {
		t.Fatalf("unrelated index changed: %q", got)
	}
	for _, ref := range []string{saved.After.Stash, saved.After.Stash + "^2"} {
		if got := gittest.Run(t, dir, "show", ref+":unrelated.txt"); got != "original\n" {
			t.Fatalf("scoped stash captured unrelated staging in %s: %q", ref, got)
		}
	}
	entries, err := NewManager().ListStashes(t.Context(), dir, 0, 20)
	testutil.FailErr(t, "list", err)
	if len(entries) != 1 || entries[0].OID != saved.After.Stash {
		t.Fatalf("stash listing: %+v", entries)
	}
	applied := operateOK(t, dir, OperationRequest{Kind: "stash", Action: "apply", Ref: entries[0].OID, ReinstateIndex: true})
	if applied.Diagnostics != "" {
		t.Fatalf("successful scoped apply exposed temporary-index diagnostics: %q", applied.Diagnostics)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "shared.txt"))
	testutil.FailErr(t, "read restored", err)
	if string(raw) != "saved\n" {
		t.Fatalf("restored: %q", raw)
	}
	if got := gittest.Run(t, dir, "show", ":shared.txt"); got != "saved\n" {
		t.Fatalf("selected staging not restored: %q", got)
	}
	if got := gittest.Run(t, dir, "show", ":unrelated.txt"); got != staged {
		t.Fatal("apply changed unrelated staging")
	}
	operateOK(t, dir, OperationRequest{Kind: "stash", Action: "drop", Ref: entries[0].OID})
	entries, err = NewManager().ListStashes(t.Context(), dir, 0, 20)
	testutil.FailErr(t, "list after drop", err)
	if len(entries) != 0 {
		t.Fatalf("stash not dropped: %+v", entries)
	}
}

func TestOperationStashConflictPreservesUnrelatedStaging(t *testing.T) {
	dir := operationFixture(t)
	operationWrite(t, dir, "shared.txt", "stashed version\n")
	gittest.Run(t, dir, "stash", "push", "--", "shared.txt")
	operationWrite(t, dir, "shared.txt", "new baseline\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	gittest.Run(t, dir, "commit", "-m", "Change baseline")
	operationWrite(t, dir, "unrelated.txt", "keep staged\n")
	gittest.Run(t, dir, "add", "--", "unrelated.txt")
	result, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "stash", Action: "apply", Ref: "stash@{0}"})
	testutil.FailErr(t, "apply conflicting stash", err)
	if result.Status != "conflicts" || len(result.After.Conflicts) != 1 || result.After.Stash == "" {
		t.Fatalf("stash conflict state: %+v", result)
	}
	if got := gittest.Run(t, dir, "show", ":unrelated.txt"); got != "keep staged\n" {
		t.Fatalf("unrelated staging changed: %q", got)
	}
	if got := gittest.Run(t, dir, "ls-files", "--unmerged"); !strings.Contains(got, "shared.txt") {
		t.Fatalf("conflict stages missing: %q", got)
	}
}

func TestOperationStashStagedDeletionWithLiteralPath(t *testing.T) {
	dir := operationFixture(t)
	path := "-literal\tline\nfile.txt"
	operationWrite(t, dir, path, "delete this\n")
	gittest.Run(t, dir, "add", "--", path)
	gittest.Run(t, dir, "commit", "-m", "Add literal path")
	gittest.Run(t, dir, "rm", "--", path)
	operationWrite(t, dir, "unrelated.txt", "keep staged\n")
	gittest.Run(t, dir, "add", "--", "unrelated.txt")
	saved := operateOK(t, dir, OperationRequest{Kind: "stash", Action: "save", Paths: []string{path}})
	if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
		testutil.FailErr(t, "saved deletion restores baseline", err)
	}
	operateOK(t, dir, OperationRequest{Kind: "stash", Action: "apply", Ref: saved.After.Stash, ReinstateIndex: true})
	if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
		t.Fatalf("stashed deletion not restored: %v", err)
	}
	if got := gittest.Run(t, dir, "diff", "--cached", "--name-only", "-z"); !strings.Contains(got, path+"\x00") {
		t.Fatalf("deletion not staged: %q", got)
	}
	if got := gittest.Run(t, dir, "show", ":unrelated.txt"); got != "keep staged\n" {
		t.Fatalf("unrelated staging changed: %q", got)
	}
}

func TestOperationMergeConflictsContinueAndAbort(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		t.Run(action, func(t *testing.T) {
			dir := operationFixture(t)
			gittest.Run(t, dir, "checkout", "-b", "feature")
			operationWrite(t, dir, "shared.txt", "feature\n")
			gittest.Run(t, dir, "add", "--", "shared.txt")
			gittest.Run(t, dir, "commit", "-m", "Feature")
			gittest.Run(t, dir, "checkout", "main")
			operationWrite(t, dir, "shared.txt", "main\n")
			gittest.Run(t, dir, "add", "--", "shared.txt")
			gittest.Run(t, dir, "commit", "-m", "Main")
			result, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "merge", Ref: "feature"})
			testutil.FailErr(t, "conflicting merge", err)
			if result.Status != "conflicts" || len(result.After.Conflicts) != 1 || result.After.MergeHead == "" {
				t.Fatalf("conflict state missing: %+v", result)
			}
			req := OperationRequest{Kind: "merge", Action: action}
			if action == "continue" {
				operationWrite(t, dir, "shared.txt", "resolved\n")
				req.Paths = []string{"shared.txt"}
			}
			done := operateOK(t, dir, req)
			if done.After.MergeHead != "" || len(done.After.Conflicts) != 0 {
				t.Fatalf("merge not settled: %+v", done)
			}
		})
	}
}

func TestOperationMergeWithoutCommitStaysActiveUntilContinue(t *testing.T) {
	dir := operationFixture(t)
	gittest.Run(t, dir, "checkout", "-b", "feature")
	operationWrite(t, dir, "feature.txt", "feature\n")
	gittest.Run(t, dir, "add", "--", "feature.txt")
	gittest.Run(t, dir, "commit", "-m", "Feature")
	gittest.Run(t, dir, "checkout", "main")
	head := strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))

	staged := operateOK(t, dir, OperationRequest{Kind: "merge", Ref: "feature", Mode: "no_ff", NoCommit: true})
	if staged.After.MergeHead == "" || staged.After.Head != head {
		t.Fatalf("no-commit merge committed or left no active merge: %+v", staged.After)
	}
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); err != nil {
		t.Fatalf("no-commit merge did not apply the merge result: %v", err)
	}
	done := operateOK(t, dir, OperationRequest{Kind: "merge", Action: "continue"})
	if done.After.MergeHead != "" || done.After.Head == head {
		t.Fatalf("continue did not record the merge: %+v", done.After)
	}
}

func TestGitLogRevisionRangesAndPaging(t *testing.T) {
	dir := operationFixture(t)
	gittest.Run(t, dir, "checkout", "-b", "feature")
	for _, message := range []string{"First", "Second"} {
		operationWrite(t, dir, "shared.txt", message+"\n")
		gittest.Run(t, dir, "add", "--", "shared.txt")
		gittest.Run(t, dir, "commit", "-m", message)
	}
	gittest.Run(t, dir, "checkout", "main")
	m := NewManager()
	commits, err := m.Log(t.Context(), dir, GitLogOpts{Ref: "main..feature", Limit: 1, Offset: 1})
	testutil.FailErr(t, "history range", err)
	if len(commits) != 1 || commits[0].Subject != "First" {
		t.Fatalf("history: %+v", commits)
	}
	refs, err := m.RevParse(t.Context(), dir, []string{"feature"})
	testutil.FailErr(t, "resolve", err)
	if len(refs) != 1 || !validObjectID(refs[0].SHA) {
		t.Fatalf("invalid resolved ref: %+v", refs)
	}
	for _, ref := range []string{"--all", "HEAD..--all", "HEAD...--all"} {
		if _, err = m.Log(t.Context(), dir, GitLogOpts{Ref: ref}); err == nil {
			t.Fatalf("option accepted: %s", ref)
		}
	}
	empty, err := m.Log(t.Context(), dir, GitLogOpts{Ref: "feature..feature"})
	testutil.FailErr(t, "empty range", err)
	if len(empty) != 0 {
		t.Fatal("identical range is not empty")
	}
	if raw, err := MarshalHistoryPage(empty, GitLogOpts{Ref: "feature..feature", Limit: 10}, nil); err != nil || !strings.Contains(raw, `"commits":[]`) {
		t.Fatalf("empty page: %s %v", raw, err)
	}
}

func TestOperationReviewsIndexOnlyChanges(t *testing.T) {
	dir := operationFixture(t)
	operationWrite(t, dir, "shared.txt", "staged\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	operationWrite(t, dir, "shared.txt", "base\n")
	oid := strings.TrimSpace(gittest.Run(t, dir, "stash", "create"))
	gittest.Run(t, dir, "restore", "--staged", "--", "shared.txt")
	reviewed := false
	operateOK(t, dir, OperationRequest{Kind: "stash", Action: "apply", Ref: oid, ReinstateIndex: true, Review: func(_ context.Context, files []RestoreFile) error {
		reviewed = true
		if len(files) != 1 || !files[0].IndexOnly || string(files[0].Before.Bytes) != "base\n" || string(files[0].After.Bytes) != "staged\n" {
			t.Fatalf("missing index-only review: %+v", files)
		}
		return nil
	}})
	if !reviewed {
		t.Fatal("index effect bypassed review")
	}
}

func TestOperationDropDuplicateStashRequiresExactSelector(t *testing.T) {
	dir := operationFixture(t)
	operationWrite(t, dir, "shared.txt", "saved\n")
	saved := operateOK(t, dir, OperationRequest{Kind: "stash", Action: "save", Paths: []string{"shared.txt"}})
	operationWrite(t, dir, "shared.txt", "another stash\n")
	operateOK(t, dir, OperationRequest{Kind: "stash", Action: "save", Paths: []string{"shared.txt"}})
	gittest.Run(t, dir, "stash", "store", "-m", "duplicate", saved.After.Stash)
	_, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "stash", Action: "drop", Ref: saved.After.Stash})
	var refusal *OperationError
	if !errors.As(err, &refusal) || refusal.Reason != "ambiguous_stash_identity" {
		t.Fatalf("ambiguous deletion allowed: %v", err)
	}
	dropped := operateOK(t, dir, OperationRequest{Kind: "stash", Action: "drop", Ref: "stash@{2}"})
	if dropped.Status != "completed" {
		t.Fatalf("stash deletion reported as no-op: %+v", dropped)
	}
	entries, err := NewManager().ListStashes(t.Context(), dir, 0, 20)
	testutil.FailErr(t, "list remaining stash", err)
	if len(entries) != 2 || entries[0].Subject != "duplicate" {
		t.Fatalf("wrong stash deleted: %+v", entries)
	}
}

func TestOperationRefusesLFSChangesBeforeBranchMoves(t *testing.T) {
	t.Setenv("LYCAON_GIT_LFS_FORCE_ABSENT", "1")
	dir := operationFixture(t)
	gittest.Run(t, dir, "checkout", "-b", "feature")
	operationWrite(t, dir, "shared.txt", "feature\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	gittest.Run(t, dir, "commit", "-m", "Feature")
	operationWrite(t, dir, ".gitattributes", "shared.txt filter=lfs\n")
	_, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "checkout", Branch: "main"})
	var refusal *OperationError
	if !errors.As(err, &refusal) || refusal.Reason != "lfs_change_unsupported" {
		t.Fatalf("LFS change not refused: %v", err)
	}
	if got := gittest.Run(t, dir, "branch", "--show-current"); got != "feature\n" {
		t.Fatalf("branch moved: %q", got)
	}
}

func TestOperationStashSavesIndexOnlyChanges(t *testing.T) {
	dir := operationFixture(t)
	operationWrite(t, dir, "shared.txt", "staged\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	operationWrite(t, dir, "shared.txt", "base\n")
	saved := operateOK(t, dir, OperationRequest{Kind: "stash", Action: "save", Paths: []string{"shared.txt"}})
	if got := gittest.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("index-only save did not clean selection: %q", got)
	}
	operateOK(t, dir, OperationRequest{Kind: "stash", Action: "apply", Ref: saved.After.Stash, ReinstateIndex: true})
	if got := gittest.Run(t, dir, "show", ":shared.txt"); got != "staged\n" {
		t.Fatalf("index-only state not restored: %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "shared.txt"))
	testutil.FailErr(t, "read working tree", err)
	if string(raw) != "base\n" {
		t.Fatalf("working-tree state changed: %q", raw)
	}
}

func TestOperationMergeAbortReviewsExistingAutostash(t *testing.T) {
	dir := operationFixture(t)
	gittest.Run(t, dir, "checkout", "-b", "feature")
	operationWrite(t, dir, "shared.txt", "feature\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	gittest.Run(t, dir, "commit", "-m", "Feature")
	gittest.Run(t, dir, "checkout", "main")
	operationWrite(t, dir, "shared.txt", "main\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	gittest.Run(t, dir, "commit", "-m", "Main")
	operationWrite(t, dir, "unrelated.txt", "saved local work\n")
	out, code, err := gitexec.Run(t.Context(), dir, []string{"merge", "--autostash", "feature"}, hermeticOpts(0))
	testutil.FailErr(t, "prepare external merge", err)
	if code != 1 {
		t.Fatalf("expected conflict: code=%d output=%s", code, out)
	}
	reviewed := false
	operateOK(t, dir, OperationRequest{Kind: "merge", Action: "abort", Review: func(_ context.Context, files []RestoreFile) error {
		for _, file := range files {
			if file.Path == "unrelated.txt" && !file.IndexOnly && string(file.After.Bytes) == "saved local work\n" {
				reviewed = true
			}
		}
		if !reviewed {
			t.Fatalf("autostash restoration missing from review: %+v", files)
		}
		return nil
	}})
	raw, err := os.ReadFile(filepath.Join(dir, "unrelated.txt"))
	testutil.FailErr(t, "read restored autostash", err)
	if string(raw) != "saved local work\n" {
		t.Fatalf("autostash work lost: %q", raw)
	}
}

func TestOperationRefusesSymlinkAncestorBeforeReadingFiles(t *testing.T) {
	dir := operationFixture(t)
	operationWrite(t, dir, "nested/file.txt", "tracked\n")
	gittest.Run(t, dir, "add", "--", "nested/file.txt")
	gittest.Run(t, dir, "commit", "-m", "Nested file")
	outside := t.TempDir()
	operationWrite(t, outside, "file.txt", "outside content\n")
	testutil.FailErr(t, "remove fixture file", os.Remove(filepath.Join(dir, "nested/file.txt")))
	testutil.FailErr(t, "remove fixture directory", os.Remove(filepath.Join(dir, "nested")))
	testutil.FailErr(t, "replace directory with link", os.Symlink(outside, filepath.Join(dir, "nested")))
	_, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "checkout", Branch: "main"})
	var refusal *OperationError
	if !errors.As(err, &refusal) || refusal.Reason != "operation_parent_not_directory" {
		t.Fatalf("symlink ancestor accepted: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(outside, "file.txt"))
	testutil.FailErr(t, "read outside fixture", err)
	if string(raw) != "outside content\n" {
		t.Fatalf("outside file changed: %q", raw)
	}
}

func TestOperationRefusesReplacementHistory(t *testing.T) {
	dir := operationFixture(t)
	original := strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))
	operationWrite(t, dir, "shared.txt", "replacement\n")
	gittest.Run(t, dir, "add", "--", "shared.txt")
	gittest.Run(t, dir, "commit", "-m", "Replacement")
	replacement := strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))
	gittest.Run(t, dir, "replace", original, replacement)
	_, err := NewManager().Operate(t.Context(), dir, OperationRequest{Kind: "checkout", Branch: "main"})
	var refusal *OperationError
	if !errors.As(err, &refusal) || refusal.Reason != "replacement_history_unsupported" {
		t.Fatalf("replacement history accepted: %v", err)
	}
}
