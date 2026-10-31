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
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestRemoveTrackedFiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		args     []string
		status   int
		retained bool
		output   string
	}{
		{"clean", []string{"rm", "--", "sub/b.txt"}, 0, false, "rm 'sub/b.txt'\n"},
		{"cached", []string{"rm", "--cached", "--", "sub/b.txt"}, 0, true, "rm 'sub/b.txt'\n"},
		{"force", []string{"rm", "-f", "--", "a.txt"}, 0, false, "rm 'a.txt'\n"},
		{"dry", []string{"rm", "--dry-run", "--", "sub/b.txt"}, 0, true, "rm 'sub/b.txt'\n"},
		{"quiet", []string{"rm", "-q", "--", "sub/b.txt"}, 0, false, ""},
		{"recursive", []string{"rm", "-r", "--", "sub"}, 0, false, "rm 'sub/b.txt'\n"},
		{"unmatched", []string{"rm", "--ignore-unmatch", "--", "absent"}, 0, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newApplyHarness(t)
			before := h.git("ls-files", "--stage")
			got := h.exec(append([]string{"git"}, test.args...)...)
			testutil.FailErr(t, "remove", got.err)
			if got.result.Status != test.status || got.stdout != test.output || got.stderr != "" {
				t.Fatalf("result %+v stdout %q stderr %q", got.result, got.stdout, got.stderr)
			}
			name := "sub/b.txt"
			if test.name == "force" {
				name = "a.txt"
			}
			if h.absent(name) == test.retained {
				t.Fatalf("retained=%v", !h.absent(name))
			}
			if test.name == "dry" || test.name == "unmatched" {
				if after := h.git("ls-files", "--stage"); after != before {
					t.Fatal("no-effect command changed index")
				}
				if got.result.EffectCertainty != toolcontract.NoEffect {
					t.Fatal("dry run reported effects")
				}
			} else if staged := h.git("diff", "--cached", "--name-only"); !strings.Contains(staged, "staged.txt") {
				t.Fatal("unrelated staging changed")
			}
		})
	}
}

func TestRemoveSafety(t *testing.T) {
	for _, test := range []struct {
		name   string
		staged bool
		work   string
		cached bool
		reason string
	}{
		{"local", false, "local\n", false, "local modifications"},
		{"staged", true, "index\n", false, "changes staged in the index"},
		{"both", true, "work\n", false, "staged content different from both the\nfile and the HEAD"},
		{"cached-both", true, "work\n", true, "staged content different from both the\nfile and the HEAD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newApplyHarness(t)
			if test.staged {
				h.write("a.txt", "index\n")
				h.git("add", "a.txt")
			}
			h.write("a.txt", test.work)
			before := h.git("ls-files", "--stage")
			args := []string{"git", "rm"}
			if test.cached {
				args = append(args, "--cached")
			}
			got := h.exec(append(args, "--", "a.txt")...)
			testutil.FailErr(t, "safety refusal", got.err)
			if got.result.Status != 1 || !strings.Contains(got.stderr, test.reason) || got.stdout != "" || got.result.EffectCertainty != toolcontract.NoEffect {
				t.Fatalf("got %+v %q %q", got.result, got.stdout, got.stderr)
			}
			if h.read("a.txt") != test.work || h.git("ls-files", "--stage") != before {
				t.Fatal("refusal had effects")
			}
		})
	}
}

func TestMoveTracksIndexContentAndSession(t *testing.T) {
	h := newApplyHarness(t)
	got := h.exec("git", "mv", "-v", "--", "a.txt", "renamed.txt")
	testutil.FailErr(t, "move", got.err)
	if got.result.Status != 0 || got.stdout != "Renaming a.txt to renamed.txt\n" || !h.absent("a.txt") || h.read("renamed.txt") != "one\nTWO\nthree\n" {
		t.Fatalf("move %+v %q %q", got.result, got.stdout, got.stderr)
	}
	if got := h.git("show", ":renamed.txt"); got != "one\ntwo\nthree\n" {
		t.Fatalf("index changed saved content %q", got)
	}
	if staged := h.git("diff", "--cached", "--name-status"); !strings.Contains(staged, "R100\ta.txt\trenamed.txt") || !strings.Contains(staged, "staged.txt") {
		t.Fatalf("staging %q", staged)
	}
	paths := sessionStaged(h.owners, h.tc.ChatSessionID(), h.root)
	if strings.Join(paths, ",") != "a.txt,renamed.txt" {
		t.Fatalf("session staging %q", paths)
	}
}

func TestMoveDirectoryAndSkip(t *testing.T) {
	h := newApplyHarness(t)
	h.write("sub/untracked", "keep\n")
	testutil.FailErr(t, "destination", os.Mkdir(filepath.Join(h.root, "destination"), 0755))
	got := h.exec("git", "mv", "-k", "--", "absent", "sub", "destination")
	testutil.FailErr(t, "move", got.err)
	if got.result.Status != 0 || !h.absent("sub") || h.read("destination/sub/b.txt") != "bee\n" || h.read("destination/sub/untracked") != "keep\n" {
		t.Fatalf("move %+v %q", got.result, got.stderr)
	}
	if staged := h.git("diff", "--cached", "--name-status"); !strings.Contains(staged, "sub/b.txt\tdestination/sub/b.txt") {
		t.Fatalf("staging %q", staged)
	}
}

func TestFileMutationDenialAndLaterIndexFailure(t *testing.T) {
	for _, command := range []string{"rm", "mv"} {
		for _, later := range []bool{false, true} {
			t.Run(command+map[bool]string{false: "-denied", true: "-index-failure"}[later], func(t *testing.T) {
				h := newApplyHarness(t)
				original := h.git("ls-files", "--stage")
				h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
					if !later || len(changes) > 0 && changes[0].Preview.Target == "index" {
						return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "fixture_denial"}}
					}
					return nil
				}
				args := []string{"git", command, "--", "sub/b.txt"}
				if command == "mv" {
					args = append(args, "moved.txt")
				}
				got := h.exec(args...)
				if got.err == nil {
					t.Fatal("expected review failure")
				}
				certainty := toolcontract.NoEffect
				if later {
					certainty = toolcontract.EffectCompleted
				}
				if got.result.EffectCertainty != certainty || h.absent("sub/b.txt") != later || h.git("ls-files", "--stage") != original {
					t.Fatalf("partial result %+v absent=%v", got.result, h.absent("sub/b.txt"))
				}
			})
		}
	}
}

func TestFileMutationDryRunAndPathRefusals(t *testing.T) {
	h := newApplyHarness(t)
	original := h.git("ls-files", "--stage")
	got := h.exec("git", "mv", "-n", "--", "sub/b.txt", "next.txt")
	testutil.FailErr(t, "dry move", got.err)
	if got.stdout != "Checking rename of 'sub/b.txt' to 'next.txt'\nRenaming sub/b.txt to next.txt\n" || got.result.EffectCertainty != toolcontract.NoEffect || h.absent("sub/b.txt") || h.git("ls-files", "--stage") != original {
		t.Fatalf("dry move %+v %q", got.result, got.stdout)
	}
	for _, args := range [][]string{{"git", "rm", "--", ":(glob)*"}, {"git", "mv", "--", "sub/b.txt", "../outside"}, {"git", "rm", "--", "../outside"}} {
		got := h.exec(args...)
		if got.err == nil || got.result.EffectCertainty != toolcontract.NoEffect {
			t.Fatalf("path refusal %+v %v", got.result, got.err)
		}
	}
}

func TestMoveForceAndSymbolicLink(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.write("destination", "replaced\n")
	got := h.exec("git", "mv", "-f", "--", "sub/b.txt", "destination")
	testutil.FailErr(t, "force move", got.err)
	if got.result.Status != 0 || got.stderr != "warning: overwriting 'destination'\n" || h.read("destination") != "bee\n" {
		t.Fatalf("force move %+v %q", got.result, got.stderr)
	}
	testutil.FailErr(t, "symbolic link", os.Symlink("a.txt", filepath.Join(h.root, "link")))
	h.git("add", "link")
	got = h.exec("git", "mv", "--", "link", "moved-link")
	testutil.FailErr(t, "move link", got.err)
	if got.result.Status != 0 {
		t.Fatalf("move link %+v %q", got.result, got.stderr)
	}
	target, err := os.Readlink(filepath.Join(h.root, "moved-link"))
	testutil.FailErr(t, "read moved link", err)
	if target != "a.txt" || !strings.HasPrefix(h.git("ls-files", "--stage", "--", "moved-link"), "120000 ") {
		t.Fatal("symbolic link type changed")
	}
	h.exec("git", "rm", "-f", "--", "moved-link").refused(t, "remove link", "COMMAND_NATIVE_FORM_UNSUPPORTED")
	if h.absent("moved-link") || !strings.HasPrefix(h.git("ls-files", "--stage", "--", "moved-link"), "120000 ") {
		t.Fatal("refused link removal changed the link or index")
	}
	got = h.exec("git", "rm", "-q", "--cached", "--", "moved-link")
	testutil.FailErr(t, "untrack link", got.err)
	if got.result.Status != 0 || h.absent("moved-link") || h.git("ls-files", "--", "moved-link") != "" {
		t.Fatalf("untrack link %+v %q", got.result, got.stderr)
	}
}

func TestRemoveMissingAndGroupedSafety(t *testing.T) {
	h := newApplyHarness(t)
	testutil.FailErr(t, "remove saved file", os.Remove(filepath.Join(h.root, "sub/b.txt")))
	got := h.exec("git", "rm", "--", "sub/b.txt")
	testutil.FailErr(t, "remove missing", got.err)
	if got.result.Status != 0 {
		t.Fatalf("missing file %+v %q", got.result, got.stderr)
	}
	h.write("sub/b.txt", "bee\n")
	h.git("add", "sub/b.txt")
	h.write("sub/b.txt", "index\n")
	h.git("add", "sub/b.txt")
	got = h.exec("git", "rm", "--", "a.txt", "sub/b.txt")
	testutil.FailErr(t, "grouped safety", got.err)
	want := "error: the following file has changes staged in the index:\n    sub/b.txt\n(use --cached to keep the file, or -f to force removal)\nerror: the following file has local modifications:\n    a.txt\n(use --cached to keep the file, or -f to force removal)\n"
	if got.result.Status != 1 || got.stderr != want {
		t.Fatalf("grouped safety %q", got.stderr)
	}
}

func TestSelectedMutationPreservesUnrelatedConflict(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"rm", "mv"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			h := newApplyHarness(t)
			_, err := h.owners.Git.Operate(t.Context(), h.root, git.OperationRequest{Kind: "stage", Entries: []git.IndexEntry{{Path: "a.txt", Mode: "100644", Stages: &[3][]byte{[]byte("base\n"), []byte("ours\n"), []byte("theirs\n")}}}})
			testutil.FailErr(t, "seed unrelated conflict", err)
			before := h.git("ls-files", "--unmerged", "--", "a.txt")
			args := []string{"git", command, "--", "sub/b.txt"}
			if command == "mv" {
				args = append(args, "moved.txt")
			}
			got := h.exec(args...)
			testutil.FailErr(t, "selected mutation", got.err)
			if got.result.Status != 0 || h.git("ls-files", "--unmerged", "--", "a.txt") != before {
				t.Fatalf("unrelated conflict changed %+v %q", got.result, got.stderr)
			}
		})
	}
}

func TestCachedRemovalRechecksSavedContentAtIndexReview(t *testing.T) {
	h := newApplyHarness(t)
	h.write("a.txt", "staged\n")
	h.git("add", "a.txt")
	before := h.git("ls-files", "--stage")
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		if len(changes) > 0 && changes[0].Preview.Target == "index" {
			h.write("a.txt", "edited during review\n")
		}
		return nil
	}
	got := h.exec("git", "rm", "--cached", "--", "a.txt")
	if got.err == nil || got.result.StopCode != "GIT_OPERATION_PRECONDITION" || got.result.EffectCertainty != toolcontract.NoEffect || h.git("ls-files", "--stage") != before {
		t.Fatalf("cached stale safety %+v %v", got.result, got.err)
	}
}

func TestFileMutationDefaultCommitLeavesUnrelatedStaging(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"rm", "mv"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			h := newApplyHarness(t)
			args := []string{"git", command, "--", "sub/b.txt"}
			if command == "mv" {
				args = append(args, "moved.txt")
			}
			got := h.exec(args...)
			testutil.FailErr(t, "tracked mutation", got.err)
			if got.result.Status != 0 {
				t.Fatalf("tracked mutation %+v %q", got.result, got.stderr)
			}
			got = h.exec("git", "commit", "-m", "track file change")
			testutil.FailErr(t, "default commit", got.err)
			if got.result.Status != 0 {
				t.Fatalf("default commit %+v %q", got.result, got.stderr)
			}
			if staged := h.git("diff", "--cached", "--name-only"); staged != "staged.txt\n" {
				t.Fatalf("unrelated staging %q", staged)
			}
			if paths := h.git("show", "--format=", "--name-status", "HEAD"); strings.Contains(paths, "staged.txt") || !strings.Contains(paths, "sub/b.txt") {
				t.Fatalf("committed paths %q", paths)
			}
		})
	}
}

type metadataMoveBackend struct {
	Backend
	blobReads int
}

func (b *metadataMoveBackend) BlobContent(context.Context, string, string, int) ([]byte, bool, error) {
	b.blobReads++
	return nil, false, fmt.Errorf("index content read was not needed")
}

func TestMoveReusesIndexBlobBeyondStreamLimit(t *testing.T) {
	h := newApplyHarness(t)
	h.write("large", strings.Repeat("x", 4096))
	h.git("add", "large")
	original := h.git("rev-parse", ":large")
	backend := &metadataMoveBackend{Backend: h.owners.Git}
	h.owners.Git = backend
	h.owners.limitOverride = map[string]int64{"stream_bytes": 512}
	got := h.exec("git", "mv", "--", "large", "renamed-large")
	testutil.FailErr(t, "metadata move", got.err)
	if got.result.Status != 0 || backend.blobReads != 0 || h.git("rev-parse", ":renamed-large") != original {
		t.Fatalf("move %+v %q reads=%d", got.result, got.stderr, backend.blobReads)
	}
}

type stageResultBackend struct {
	Backend
	result  git.OperationResult
	failure error
}

func (b stageResultBackend) Operate(context.Context, string, git.OperationRequest) (git.OperationResult, error) {
	return b.result, b.failure
}

func TestIndexAttemptCertaintyUsesObservedState(t *testing.T) {
	for _, test := range []struct {
		name                         string
		attempted, observed, changed bool
		certainty                    toolcontract.EffectCertainty
	}{
		{"before-attempt", false, false, false, toolcontract.NoEffect},
		{"unobserved", true, false, false, toolcontract.EffectUnknown},
		{"observed-unchanged", true, true, false, toolcontract.NoEffect},
		{"observed-changed", true, true, true, toolcontract.EffectCompleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newApplyHarness(t)
			result := git.OperationResult{Attempted: test.attempted, AfterObserved: test.observed, Status: "failed", Before: git.RepositoryState{Index: "before"}, After: git.RepositoryState{Index: "before"}}
			if test.changed {
				result.After.Index = "after"
			}
			h.owners.Git = stageResultBackend{Backend: h.owners.Git, result: result, failure: fmt.Errorf("fixture index failure")}
			got := h.exec("git", "rm", "-f", "--cached", "--", "sub/b.txt")
			if got.result.EffectCertainty != test.certainty || h.absent("sub/b.txt") {
				t.Fatalf("certainty %+v", got.result)
			}
		})
	}
}

func TestMoveRefusesDestinationCreatedDuringReview(t *testing.T) {
	h := newApplyHarness(t)
	before := h.git("ls-files", "--stage")
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		if len(changes) > 0 && changes[0].Preview.Target != "index" {
			h.write("destination", "created during review\n")
		}
		return nil
	}
	got := h.exec("git", "mv", "--", "sub/b.txt", "destination")
	if got.result.StopCode != "GIT_OPERATION_PRECONDITION" || got.result.EffectCertainty != toolcontract.NoEffect || h.read("sub/b.txt") != "bee\n" || h.read("destination") != "created during review\n" || h.git("ls-files", "--stage") != before {
		t.Fatalf("concurrent destination %+v %v", got.result, got.err)
	}
}

func TestMoveDirectoryVerboseAndDryRun(t *testing.T) {
	for _, flag := range []string{"-v", "-n"} {
		t.Run(flag, func(t *testing.T) {
			h := newApplyHarness(t)
			got := h.exec("git", "mv", flag, "--", "sub", "renamed")
			testutil.FailErr(t, "directory move", got.err)
			want := "Renaming sub to renamed\nRenaming sub/b.txt to renamed/b.txt\n"
			if flag == "-n" {
				want = "Checking rename of 'sub' to 'renamed'\nChecking rename of 'sub/b.txt' to 'renamed/b.txt'\n" + want
			}
			if got.result.Status != 0 || got.stdout != want {
				t.Fatalf("directory move %+v %q %q", got.result, got.stdout, got.stderr)
			}
		})
	}
}

func TestRemoveSelectedConflict(t *testing.T) {
	h := newApplyHarness(t)
	_, err := h.owners.Git.Operate(t.Context(), h.root, git.OperationRequest{Kind: "stage", Entries: []git.IndexEntry{{Path: "a.txt", Mode: "100644", Stages: &[3][]byte{[]byte("base\n"), []byte("ours\n"), []byte("theirs\n")}}}})
	testutil.FailErr(t, "seed conflict", err)
	got := h.exec("git", "rm", "--", "a.txt")
	testutil.FailErr(t, "remove selected conflict", got.err)
	if got.result.Status != 0 || !h.absent("a.txt") || h.git("ls-files", "--unmerged", "--", "a.txt") != "" || !strings.Contains(h.git("diff", "--cached", "--name-only"), "staged.txt") {
		t.Fatalf("conflict removal %+v %q", got.result, got.stderr)
	}
}

func TestRemovePrunesEmptiedDirectoriesUpToTheWorkingDirectory(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	for _, name := range []string{"deep/a/b/leaf", "deep/a/kept/other", "cwd/inner/leaf", "cwd/sibling"} {
		h.write(name, name+"\n")
	}
	h.git("add", ".")
	h.git("commit", "-q", "-m", "nested")
	got := h.exec("git", "rm", "-q", "--", "deep/a/b/leaf", "deep/a/kept/other")
	testutil.FailErr(t, "remove nested", got.err)
	if got.result.Status != 0 || !h.absent("deep") {
		t.Fatalf("remove %+v %q; deep retained=%v", got.result, got.stderr, !h.absent("deep"))
	}
	var err error
	h.paths, err = projectpaths.NewProgramPaths(t.Context(), h.owners.Boundary, h.tc, filepath.Join(h.root, "cwd", "inner"))
	testutil.FailErr(t, "working directory", err)
	got = h.exec("git", "rm", "-q", "--", "leaf")
	testutil.FailErr(t, "remove in working directory", got.err)
	if got.result.Status != 0 || h.absent("cwd/inner") || !h.absent("cwd/inner/leaf") {
		t.Fatalf("remove %+v %q; working directory removed=%v", got.result, got.stderr, h.absent("cwd/inner"))
	}
	if staged := h.git("diff", "--cached", "--name-status"); staged != "D\tcwd/inner/leaf\nD\tdeep/a/b/leaf\nD\tdeep/a/kept/other\n" {
		t.Fatalf("staged %q", staged)
	}
}

func TestCachedRemovalAndMoveKeepParentDirectories(t *testing.T) {
	h := newApplyHarness(t)
	got := h.exec("git", "rm", "-q", "--cached", "--", "sub/b.txt")
	testutil.FailErr(t, "cached removal", got.err)
	if got.result.Status != 0 || h.absent("sub/b.txt") {
		t.Fatalf("cached removal %+v %q", got.result, got.stderr)
	}
	h.git("add", "sub/b.txt")
	got = h.exec("git", "mv", "--", "sub/b.txt", "moved.txt")
	testutil.FailErr(t, "move", got.err)
	if got.result.Status != 0 || h.absent("sub") {
		t.Fatalf("move %+v %q; source directory removed=%v", got.result, got.stderr, h.absent("sub"))
	}
}
