package worktrees_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func seedActorSessions(t *testing.T, sqlDB *db.Store, rootSessionID string, childSessionIDs ...string) {
	t.Helper()
	testdbseed.InsertSession(t, sqlDB, rootSessionID, testdbseed.DefaultProjectID)
	for _, child := range childSessionIDs {
		testdbseed.InsertChildSession(t, sqlDB, child, rootSessionID)
	}
}

type flakyLister struct {
	failNext atomic.Bool
	calls    atomic.Int32
}

func (f *flakyLister) ListWorktrees(ctx context.Context, toplevel string) ([]git.WorktreeEntry, error) {
	f.calls.Add(1)
	if f.failNext.CompareAndSwap(true, false) {
		return nil, errors.New("simulated transient git worktree list failure")
	}
	return git.NewManager().ListWorktrees(ctx, toplevel)
}

func TestRecordUsageIdempotentReplayRetryAndExecutionBindingIndependence(t *testing.T) {
	t.Parallel()
	base, primary := initGitFixture(t)
	sqlDB := openInventoryStore(t, primary)
	seedActorSessions(t, sqlDB, "chat-1", "worker-sess-1", "worker-sess-2")
	repoID := git.DeriveRepoID(primary)
	spec := worktrees.RepoSpec{
		ProjectID: testdbseed.DefaultProjectID,
		RepoID:    repoID,
		Toplevel:  primary,
	}

	lister := &flakyLister{}
	lister.failNext.Store(true)
	inv := worktrees.New(sqlDB, worktrees.Options{Git: lister})

	sibling1 := filepath.Join(base, "wt-agent-1")
	runGitCmd(t, primary, "worktree", "add", "-q", "-b", "feat/agent-1", sibling1)

	// First delivery succeeds and records registration + chat creation even though background discovery fails once.
	first, err := inv.RecordUsage(t.Context(), worktrees.UsageInput{
		ProjectID:      testdbseed.DefaultProjectID,
		TargetPath:     sibling1,
		RootSessionID:  "chat-1",
		ActorSessionID: "worker-sess-1",
		WorkerID:       "worker-1",
		ActivityID:     "act-create-1",
		Relation:       worktrees.UsageRelationCreated,
	})
	testutil.FailErr(t, "record created usage", err)
	if !first.Recorded || first.Duplicate || first.Checkout.ID == "" {
		t.Fatalf("unexpected first outcome: %+v", first)
	}
	testutil.WaitFor(t, "background discovery attempt", func() bool {
		return lister.calls.Load() >= 1
	})

	// Duplicate replay of the same activity ID produces no second registration or receipt.
	replay, err := inv.RecordUsage(t.Context(), worktrees.UsageInput{
		ProjectID:      testdbseed.DefaultProjectID,
		TargetPath:     sibling1,
		RootSessionID:  "chat-1",
		ActorSessionID: "worker-sess-1",
		WorkerID:       "worker-1",
		ActivityID:     "act-create-1",
		Relation:       worktrees.UsageRelationCreated,
	})
	testutil.FailErr(t, "replay created usage", err)
	if !replay.Recorded || !replay.Duplicate || replay.Checkout.ID != first.Checkout.ID {
		t.Fatalf("unexpected replay outcome: %+v (want checkout %s)", replay, first.Checkout.ID)
	}

	receiptCount, err := db.New(sqlDB).CountSourceWorktreeUsageReceipts(t.Context(), db.CountSourceWorktreeUsageReceiptsParams{
		RootSessionID: "chat-1",
		WorktreeID:    first.Checkout.ID,
		Relation:      string(worktrees.UsageRelationCreated),
	})
	testutil.FailErr(t, "count receipts", err)
	if receiptCount != 1 {
		t.Fatalf("created receipt count = %d, want 1", receiptCount)
	}

	// Session execution binding (session_worktrees) remains untouched.
	repos := store.NewSQL(sqlDB)
	if binding, bound, err := repos.Sessions.GetWorktreeBinding(t.Context(), "chat-1"); err != nil || bound {
		t.Fatalf("creating or using a worktree mutated session execution binding: bound=%v binding=%+v err=%v", bound, binding, err)
	}

	// Second worker creates/uses a second sibling; external shell creates a third sibling.
	sibling2 := filepath.Join(base, "wt-agent-2")
	runGitCmd(t, primary, "worktree", "add", "-q", "-b", "feat/agent-2", sibling2)
	second, err := inv.RecordUsage(t.Context(), worktrees.UsageInput{
		ProjectID:      testdbseed.DefaultProjectID,
		TargetPath:     filepath.Join(sibling2, "packages", "a", "app.go"),
		RootSessionID:  "chat-1",
		ActorSessionID: "worker-sess-2",
		WorkerID:       "worker-2",
		ActivityID:     "act-use-2",
		Relation:       worktrees.UsageRelationUsed,
	})
	testutil.FailErr(t, "record worker-2 usage", err)
	if !second.Recorded || second.Checkout.ID == first.Checkout.ID {
		t.Fatalf("expected distinct checkout ID for second sibling: %+v", second)
	}

	externalWT := filepath.Join(base, "wt-external-shell")
	runGitCmd(t, primary, "worktree", "add", "-q", "--detach", externalWT)

	// Refresh reconciles missed background discovery and discovers externalWT without inventing a creator.
	items, err := inv.Refresh(t.Context(), spec)
	testutil.FailErr(t, "refresh inventory", err)
	extCheckout, ok := findCheckoutByPath(items, externalWT)
	if !ok {
		t.Fatalf("external checkout missing from inventory: %+v", items)
	}

	usages, err := inv.SummarizeChatRepoUsages(t.Context(), testdbseed.DefaultProjectID, repoID, "chat-1")
	testutil.FailErr(t, "summarize usages", err)
	if _, fabricated := usages[extCheckout.ID]; fabricated {
		t.Fatalf("external checkout fabricated chat association before use: %+v", usages[extCheckout.ID])
	}
	u1 := usages[first.Checkout.ID]
	if !u1.CreatedByChat || !u1.UsedByChat || len(u1.WorkerIDs) != 1 || u1.WorkerIDs[0] != "worker-1" {
		t.Fatalf("unexpected usage summary for sibling1: %+v", u1)
	}
	u2 := usages[second.Checkout.ID]
	if u2.CreatedByChat || !u2.UsedByChat || len(u2.WorkerIDs) != 1 || u2.WorkerIDs[0] != "worker-2" {
		t.Fatalf("unexpected usage summary for sibling2: %+v", u2)
	}

	// Later verified action in externalWT associates use without fabricating creation.
	later, err := inv.RecordUsage(t.Context(), worktrees.UsageInput{
		ProjectID:      testdbseed.DefaultProjectID,
		TargetPath:     externalWT,
		RootSessionID:  "chat-1",
		ActorSessionID: "chat-1",
		ActivityID:     "act-ext-use",
		Relation:       worktrees.UsageRelationUsed,
	})
	testutil.FailErr(t, "record later external usage", err)
	if !later.Recorded || later.Checkout.ID != extCheckout.ID {
		t.Fatalf("later action did not attach to existing external checkout %s: %+v", extCheckout.ID, later)
	}
	usagesAfter, err := inv.SummarizeChatRepoUsages(t.Context(), testdbseed.DefaultProjectID, repoID, "chat-1")
	testutil.FailErr(t, "summarize usages after external use", err)
	uExt := usagesAfter[extCheckout.ID]
	if uExt.CreatedByChat || !uExt.UsedByChat {
		t.Fatalf("external checkout should record UsedByChat=true CreatedByChat=false, got %+v", uExt)
	}

	// Unattached repository and nested unrelated repository never record associations.
	unrelated := filepath.Join(base, "unattached-repo")
	testutil.FailErr(t, "mkdir unattached", os.MkdirAll(unrelated, 0o755))
	runGitCmd(t, unrelated, "init", "-q", "-b", "main")
	unrelOut, err := inv.RecordUsage(t.Context(), worktrees.UsageInput{
		ProjectID:      testdbseed.DefaultProjectID,
		TargetPath:     unrelated,
		RootSessionID:  "chat-1",
		ActorSessionID: "chat-1",
		ActivityID:     "act-unrelated",
		Relation:       worktrees.UsageRelationUsed,
	})
	testutil.FailErr(t, "record unattached repo", err)
	if unrelOut.Recorded {
		t.Fatalf("unattached repository leaked into worktree usage: %+v", unrelOut)
	}

	nested := filepath.Join(sibling1, "nested-unrelated")
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(nested, 0o755))
	runGitCmd(t, nested, "init", "-q", "-b", "main")
	nestedOut, err := inv.RecordUsage(t.Context(), worktrees.UsageInput{
		ProjectID:      testdbseed.DefaultProjectID,
		TargetPath:     nested,
		RootSessionID:  "chat-1",
		ActorSessionID: "chat-1",
		ActivityID:     "act-nested",
		Relation:       worktrees.UsageRelationUsed,
	})
	testutil.FailErr(t, "record nested repo", err)
	if nestedOut.Recorded {
		t.Fatalf("nested unrelated repository matched parent sibling: %+v", nestedOut)
	}
}
