//go:build darwin || linux

package toolhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
}

func TestCommandWorktreeCreationUsageAndUnauthorizedRefusal(t *testing.T) {
	fixture, _, _, _ := composedMutationEntry(t)
	runFixtureGit(t, fixture.Dir, "init", "-q", "-b", "main")
	testutil.FailErr(t, "seed readme", os.WriteFile(filepath.Join(fixture.Dir, "readme.txt"), []byte("v1\n"), 0o644))
	runFixtureGit(t, fixture.Dir, "add", "readme.txt")
	runFixtureGit(t, fixture.Dir, "commit", "-qm", "init")

	inv := worktrees.New(fixture.Database, worktrees.Options{})
	recorder := worktrees.NewToolUsageRecorder(inv)

	allowedParent := gitrepo.CanonicalDir(t.TempDir())
	deniedParent := gitrepo.CanonicalDir(t.TempDir())
	allowedWT := filepath.Join(allowedParent, "wt-allowed")
	deniedWT := filepath.Join(deniedParent, "wt-denied")

	var activeWorker string
	baseConfigure := fixture.Configure
	fixture.Configure = func(tc *tools.ToolContext) {
		baseConfigure(tc)
		tc.Source.CheckoutUsage = recorder
		tc.Identity.WorkerJobID = activeWorker
		tc.Files.GrantedWriteRoots = []string{allowedParent}
		tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{
			{Path: allowedParent, Access: hitl.GrantedPathReadWrite},
		}
	}

	// 1. Unauthorized sibling path fails before creating a registration or chat association.
	refusedRaw, refusedErr := fixture.Runtime.Run(t.Context(), "command", map[string]any{
		"command": "git worktree add -b feat/denied " + deniedWT,
	}, fixture.ToolContext())
	if refusedErr == nil {
		outcome := settledProgramOutcome(t, fixture, refusedRaw)
		if outcome.Status == 0 {
			t.Fatalf("expected unauthorized git worktree add to fail, got %+v", outcome)
		}
	}
	repoID := git.DeriveRepoID(fixture.Dir)
	usagesBefore, err := inv.SummarizeChatRepoUsages(t.Context(), testdbseed.DefaultProjectID, repoID, fixture.Session)
	testutil.FailErr(t, "summarize usages after refusal", err)
	if len(usagesBefore) != 0 {
		t.Fatalf("unauthorized git worktree add created usage association: %+v", usagesBefore)
	}
	if _, statErr := os.Stat(deniedWT); !os.IsNotExist(statErr) {
		t.Fatalf("unauthorized worktree directory was created: %v", statErr)
	}

	// 2. Authorized native `git worktree add` records creation + use without binding session_worktrees.
	activeWorker = "worker-a"
	addOutcome := callComposedProgram(t, fixture, "git worktree add -b feat/allowed "+allowedWT)
	if addOutcome.Status != 0 || addOutcome.Refusal != nil {
		t.Fatalf("authorized git worktree add failed: %+v refusal=%+v", addOutcome, addOutcome.Refusal)
	}

	repos := store.NewSQL(fixture.Database)
	if _, bound, bindErr := repos.Sessions.GetWorktreeBinding(t.Context(), fixture.Session); bindErr != nil || bound {
		t.Fatalf("git worktree add mutated session_worktrees binding: bound=%v err=%v", bound, bindErr)
	}

	// 3. External worktree created outside the tool host has no fabricated creator, then worker-b uses it via cd + edit.
	externalWT := filepath.Join(allowedParent, "wt-external")
	runFixtureGit(t, fixture.Dir, "worktree", "add", "-q", "-b", "feat/external", externalWT)
	items, err := inv.Refresh(t.Context(), worktrees.RepoSpec{
		ProjectID: testdbseed.DefaultProjectID,
		RepoID:    repoID,
		Toplevel:  fixture.Dir,
	})
	testutil.FailErr(t, "refresh checkouts", err)
	var allowedID, externalID string
	for _, item := range items {
		switch item.WorktreePath {
		case allowedWT:
			allowedID = item.ID
		case externalWT:
			externalID = item.ID
		}
	}
	if allowedID == "" || externalID == "" {
		t.Fatalf("expected both allowed and external checkouts in %+v", items)
	}

	usagesMid, err := inv.SummarizeChatRepoUsages(t.Context(), testdbseed.DefaultProjectID, repoID, fixture.Session)
	testutil.FailErr(t, "summarize mid usages", err)
	if !usagesMid[allowedID].CreatedByChat || !usagesMid[allowedID].UsedByChat {
		t.Fatalf("allowed checkout missing created+used association: %+v", usagesMid[allowedID])
	}
	if _, exists := usagesMid[externalID]; exists {
		t.Fatalf("external checkout had fabricated chat association before use: %+v", usagesMid[externalID])
	}

	activeWorker = "worker-b"
	useOutcome := callComposedProgram(t, fixture, "cd "+externalWT+" && edit --path readme.txt --old-string v1 --new-string v2")
	if useOutcome.Status != 0 || useOutcome.Refusal != nil {
		t.Fatalf("worker-b cd + edit in external worktree failed: %+v refusal=%+v", useOutcome, useOutcome.Refusal)
	}

	usagesAfter, err := inv.SummarizeChatRepoUsages(t.Context(), testdbseed.DefaultProjectID, repoID, fixture.Session)
	testutil.FailErr(t, "summarize final usages", err)
	if usagesAfter[externalID].CreatedByChat || !usagesAfter[externalID].UsedByChat {
		t.Fatalf("external checkout usage mismatch: %+v", usagesAfter[externalID])
	}
	if len(usagesAfter[allowedID].WorkerIDs) != 1 || usagesAfter[allowedID].WorkerIDs[0] != "worker-a" {
		t.Fatalf("worker-a attribution mismatch: %+v", usagesAfter[allowedID])
	}
	if len(usagesAfter[externalID].WorkerIDs) != 1 || usagesAfter[externalID].WorkerIDs[0] != "worker-b" {
		t.Fatalf("worker-b attribution mismatch: %+v", usagesAfter[externalID])
	}
}
