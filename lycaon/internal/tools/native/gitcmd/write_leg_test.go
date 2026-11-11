package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// gitReadSpellings is one admitted spelling of every read: the read forms, and
// the mutation forms that read.
var gitReadSpellings = map[string][]string{
	"git status":          {"git", "status"},
	"git diff":            {"git", "diff", "--", "src/main.go"},
	"git log":             {"git", "log"},
	"git show":            {"git", "show"},
	"git blame":           {"git", "blame", "src/main.go"},
	"git branch":          {"git", "branch"},
	"git tag":             {"git", "tag", "-l"},
	"git rev-parse":       {"git", "rev-parse", "--show-toplevel"},
	"git rev-list":        {"git", "rev-list", "HEAD"},
	"git merge-base":      {"git", "merge-base", "main", "topic"},
	"git remote":          {"git", "remote"},
	"git config":          {"git", "config", "--list"},
	"git stash list":      {"git", "stash", "list"},
	"git worktree list":   {"git", "worktree", "list"},
	"git ls-files":        {"git", "ls-files"},
	"git grep":            {"git", "grep", "needle"},
	"git describe":        {"git", "describe"},
	"git reflog":          {"git", "reflog"},
	"git shortlog":        {"git", "shortlog"},
	"git ls-tree":         {"git", "ls-tree", "HEAD"},
	"git cat-file":        {"git", "cat-file", "-t", "HEAD"},
	"git show-ref":        {"git", "show-ref"},
	"git for-each-ref":    {"git", "for-each-ref"},
	"git stash show":      {"git", "stash", "show"},
	"git clean -n":        {"git", "clean", "-n"},
	"git ls-remote":       {"git", "ls-remote", "origin"},
	"git bisect log":      {"git", "bisect", "log"},
	"git bisect terms":    {"git", "bisect", "terms"},
	"git bisect visual":   {"git", "bisect", "visualize", "--oneline"},
	"git bisect view":     {"git", "bisect", "view", "--oneline"},
	"git -C with no repo": {"git", "-C", "plain", "status"},
}

type writeLegBranch struct{}

func (writeLegBranch) ValidateMeta(context.Context) error { return nil }

func (writeLegBranch) EnsureParents(context.Context, string) error { return nil }

// watchNoRepository installs the program's shared record on the harness.
func watchNoRepository(h *harness) *atomic.Bool {
	missed := &atomic.Bool{}
	h.tc.Effects.GitNoRepository = missed
	return missed
}

// newWriteLegHarness is a worker branch holding a plain directory and no repository.
func newWriteLegHarness(t *testing.T) (*harness, *atomic.Bool) {
	t.Helper()
	h := newUninitializedHarness(t)
	h.tc.Source.SourceWorkspaceKind, h.tc.Identity.WorkerJobID = api.SourceWorkspaceKindWorker, "job-1"
	h.tc.Source.WorkerBranchRoot, h.tc.Source.BranchWorkspace = h.root, tools.BranchWorkspace(writeLegBranch{})
	paths, err := projectpaths.NewProgramPaths(t.Context(), h.owners.Boundary, h.tc, "")
	testutil.FailErr(t, "worker program paths", err)
	h.paths = paths
	testutil.FailErr(t, "plain dir", os.MkdirAll(filepath.Join(h.root, "plain"), 0o755))
	return h, watchNoRepository(h)
}

func TestACOP01EveryGitReadOnAWriteLegKeepsGitsStatusAndStatesTheMiss(t *testing.T) {
	t.Parallel()
	for name, argv := range gitReadSpellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, missed := newWriteLegHarness(t)
			got := h.exec(argv...)
			if got.err != nil || got.result.Status != 128 || got.result.StopCode != "" || got.stderr == "" {
				t.Fatalf("%s = %+v stderr %q err %v, want Git's status 128", name, got.result, got.stderr, got.err)
			}
			if !missed.Load() {
				t.Fatalf("%s did not state the miss", name)
			}
		})
	}
}

func TestACOP03EveryGitReadOffAWorkerBranchStatesNoMiss(t *testing.T) {
	t.Parallel()
	for name, argv := range gitReadSpellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newUninitializedHarness(t)
			missed := watchNoRepository(h)
			testutil.FailErr(t, "plain dir", os.MkdirAll(filepath.Join(h.root, "plain"), 0o755))
			got := h.exec(argv...)
			if got.err != nil || got.result.Status != 128 || got.result.StopCode != "" || got.stderr == "" {
				t.Fatalf("%s = %+v stderr %q err %v, want Git's status 128", name, got.result, got.stderr, got.err)
			}
			if missed.Load() {
				t.Fatalf("%s stated a miss off a worker branch", name)
			}
		})
	}
}

func TestWriteLegMissNeedsAReadWithNoRepository(t *testing.T) {
	t.Parallel()
	t.Run("a repository the worker made reads normally", func(t *testing.T) {
		t.Parallel()
		h, missed := newWriteLegHarness(t)
		h.git("init", "-q", "plain")
		if got := h.exec("git", "-C", "plain", "status", "-s"); got.err != nil || got.result.Status != 0 || missed.Load() {
			t.Fatalf("status in the worker's repository = %+v err %v missed %v", got.result, got.err, missed.Load())
		}
		testutil.FailErr(t, "second plain dir", os.MkdirAll(filepath.Join(h.root, "other"), 0o755))
		if got := h.exec("git", "-C", "other", "status"); got.result.Status != 128 || !missed.Load() {
			t.Fatalf("directory beside the repository = %+v missed %v", got.result, missed.Load())
		}
	})
	t.Run("a mutation states no miss", func(t *testing.T) {
		t.Parallel()
		h, missed := newWriteLegHarness(t)
		h.write("a.txt", "a\n")
		if got := h.exec("git", "add", "a.txt"); got.err != nil || got.result.Status != 128 || missed.Load() {
			t.Fatalf("git add = %+v err %v missed %v", got.result, got.err, missed.Load())
		}
	})
	t.Run("a missing directory states no miss", func(t *testing.T) {
		t.Parallel()
		h, missed := newWriteLegHarness(t)
		if got := h.exec("git", "-C", "nope", "status"); got.err != nil || got.result.Status != 128 || missed.Load() {
			t.Fatalf("missing -C = %+v err %v missed %v", got.result, got.err, missed.Load())
		}
	})
}
