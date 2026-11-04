package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

const noRepositoryOnWriteLeg = "GIT_NO_REPOSITORY_ON_WRITE_LEG"

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

// newWriteLegHarness is a worker branch holding a plain directory and no repository.
func newWriteLegHarness(t *testing.T) *harness {
	t.Helper()
	h := newUninitializedHarness(t)
	h.tc.Source.SourceWorkspaceKind, h.tc.Identity.WorkerJobID = api.SourceWorkspaceKindWorker, "job-1"
	h.tc.Source.WorkerBranchRoot, h.tc.Source.BranchWorkspace = h.root, tools.BranchWorkspace(writeLegBranch{})
	paths, err := projectpaths.NewProgramPaths(t.Context(), h.owners.Boundary, h.tc, "")
	testutil.FailErr(t, "worker program paths", err)
	h.paths = paths
	testutil.FailErr(t, "plain dir", os.MkdirAll(filepath.Join(h.root, "plain"), 0o755))
	return h
}

func TestGitReadSpellingsCoverEveryReadForm(t *testing.T) {
	t.Parallel()
	for form := range readForms {
		if _, ok := gitReadSpellings[form]; !ok {
			t.Errorf("read form %s has no spelling in the write-leg contract", form)
		}
	}
}

func TestACOP01AndACOP02EveryGitReadOnAWriteLegRefuses(t *testing.T) {
	t.Parallel()
	for name, argv := range gitReadSpellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := newWriteLegHarness(t).exec(argv...)
			got.refused(t, name, noRepositoryOnWriteLeg)
			if got.result.Terminal != toolcontract.ProducerRefused || got.result.StopCode != noRepositoryOnWriteLeg || got.stderr != "" {
				t.Fatalf("%s = %+v stderr %q, want a refusal in place of Git's fatal", name, got.result, got.stderr)
			}
		})
	}
}

func TestACOP03EveryGitReadOffAWorkerBranchKeepsGitsStatus(t *testing.T) {
	t.Parallel()
	for name, argv := range gitReadSpellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newUninitializedHarness(t)
			testutil.FailErr(t, "plain dir", os.MkdirAll(filepath.Join(h.root, "plain"), 0o755))
			got := h.exec(argv...)
			if got.err != nil || got.result.Status != 128 || got.result.StopCode != "" || got.stderr == "" {
				t.Fatalf("%s = %+v stderr %q err %v, want Git's status 128", name, got.result, got.stderr, got.err)
			}
		})
	}
}

func TestWriteLegRefusalStatesWhetherTheProfileHoldsSourceHistory(t *testing.T) {
	t.Parallel()
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "holds", Tools: map[string]bool{"source_history": true}},
		{ID: "lacks", Tools: map[string]bool{"edit": true}},
	})
	for _, tc := range []struct {
		profile string
		want    bool
	}{{"holds", true}, {"lacks", false}} {
		t.Run(tc.profile, func(t *testing.T) {
			t.Parallel()
			h := newWriteLegHarness(t)
			h.owners.Boundary, h.tc.Identity.Agent = boundary, tc.profile
			got := h.exec("git", "status")
			got.refused(t, tc.profile, noRepositoryOnWriteLeg)
			if data := toolrejection.AsToolReject(got.err).Data; data["source_history_offered"] != tc.want {
				t.Fatalf("data = %v, want source_history_offered %v", data, tc.want)
			}
		})
	}
}

func TestWriteLegRefusalNeedsAReadWithNoRepository(t *testing.T) {
	t.Parallel()
	t.Run("a repository the worker made reads normally", func(t *testing.T) {
		t.Parallel()
		h := newWriteLegHarness(t)
		h.git("init", "-q", "plain")
		if got := h.exec("git", "-C", "plain", "status", "-s"); got.err != nil || got.result.Status != 0 {
			t.Fatalf("status in the worker's repository = %+v err %v", got.result, got.err)
		}
		testutil.FailErr(t, "second plain dir", os.MkdirAll(filepath.Join(h.root, "other"), 0o755))
		beside := h.exec("git", "-C", "other", "status")
		beside.refused(t, "directory beside the repository", noRepositoryOnWriteLeg)
	})
	t.Run("a mutation keeps Git's status", func(t *testing.T) {
		t.Parallel()
		h := newWriteLegHarness(t)
		h.write("a.txt", "a\n")
		if got := h.exec("git", "add", "a.txt"); got.err != nil || got.result.Status != 128 {
			t.Fatalf("git add = %+v err %v, want status 128", got.result, got.err)
		}
	})
	t.Run("a missing directory keeps Git's status", func(t *testing.T) {
		t.Parallel()
		if got := newWriteLegHarness(t).exec("git", "-C", "nope", "status"); got.err != nil || got.result.Status != 128 {
			t.Fatalf("missing -C = %+v err %v, want status 128", got.result, got.err)
		}
	})
}
