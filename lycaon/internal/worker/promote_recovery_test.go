package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// testPromoteCrashAfterCommit crashes between CommitPromotion and tx.close and
// recovers through the boot entry, whose only commit evidence is the job row.
func testPromoteCrashAfterCommit(t *testing.T) {
	ctx := t.Context()
	setup := func(t *testing.T, id string, status api.WorkerMergeStatus) (*MergeService, *promoteCrashFixture) {
		t.Helper()
		sqlDB := testdbfixture.Open(t, "store.db")
		queue := NewSQLQueue(sqlDB, 8)
		root := t.TempDir()
		testdbseed.InsertSessionWithRoot(t, sqlDB, "parent", testdbseed.DefaultProjectID, root)
		_, err := queue.Enqueue(ctx, api.WorkerTask{ID: id, ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: root, AgentType: "implementer", Prompt: "p", Brief: "b", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}})
		testutil.FailErr(t, "enqueue", err)
		testutil.FailErr(t, "set merge status", queue.SetMergeStatus(ctx, id, status))
		svc := &MergeService{Queue: queue, Store: queue, DataDir: t.TempDir()}
		task, ok := queue.Get(id)
		if !ok {
			t.Fatal("job not found")
		}
		f := newPromoteCrashFixtureIn(t, svc.hostDataDirFor(task))
		f.task.ID = id
		testutil.FailErr(t, "seed stale slot for job", os.MkdirAll(promoteTransactionRetainedDir(f.txPath()), 0o700))
		if err := f.run(t, -1, nil); err != nil {
			t.Fatalf("apply: %v", err)
		}
		return svc, f
	}
	assertLanded := func(t *testing.T, f *promoteCrashFixture) {
		t.Helper()
		if raw, err := os.ReadFile(f.edited); err != nil || string(raw) != "new-edit\n" {
			t.Fatalf("landed edit = %q, %v", raw, err)
		}
		if raw, err := os.ReadFile(f.created); err != nil || string(raw) != "new-created\n" {
			t.Fatalf("landed create = %q, %v", raw, err)
		}
	}
	t.Run("merged job settles without rollback", func(t *testing.T) {
		svc, f := setup(t, "job-committed", api.WorkerMergeStatusMerged)
		testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
		for _, path := range []string{f.txPath(), promoteTransactionRetainedDir(f.txPath())} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s survived settlement: %v", filepath.Base(path), err)
			}
		}
		assertLanded(t, f)
		alias, err := os.Stat(f.alias)
		testutil.FailErr(t, "stat alias", err)
		if !os.SameFile(alias, f.originA) {
			t.Fatal("alias lost the original inode")
		}
	})
	for _, status := range []api.WorkerMergeStatus{api.WorkerMergeStatusPending, api.WorkerMergeStatusRejected, api.WorkerMergeStatusAborted} {
		t.Run(string(status)+" job keeps its rollback authority", func(t *testing.T) {
			svc, f := setup(t, "job-uncommitted", status)
			testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
			if _, err := os.Lstat(f.txPath()); err != nil {
				t.Fatalf("uncommitted journal removed: %v", err)
			}
			if _, err := os.Lstat(f.slot("edited.sh")); err != nil {
				t.Fatalf("uncommitted slot removed: %v", err)
			}
		})
	}
	t.Run("journal without a known job is kept", func(t *testing.T) {
		svc, f := setup(t, "job-known", api.WorkerMergeStatusMerged)
		foreign := promoteTransactionPath(f.dataDir, "job-unknown")
		testutil.FailErr(t, "write foreign journal", writePromoteTransaction(foreign, promoteTransactionJournal{Version: promoteTransactionVersion, JobID: "job-unknown", Phase: promotePhaseApplying}))
		testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
		if _, err := os.Lstat(foreign); err != nil {
			t.Fatalf("journal of an unknown job removed: %v", err)
		}
	})
	t.Run("journal copied from another job is kept", func(t *testing.T) {
		svc, f := setup(t, "job-merged", api.WorkerMergeStatusMerged)
		raw, err := os.ReadFile(f.txPath())
		testutil.FailErr(t, "read journal", err)
		misnamed := promoteTransactionPath(f.dataDir, "job-other")
		testutil.FailErr(t, "write misnamed journal", os.WriteFile(misnamed, raw, 0o600))
		testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
		if _, err := os.Lstat(misnamed); err != nil {
			t.Fatalf("misnamed journal removed: %v", err)
		}
	})
	t.Run("slots without a journal are inert", func(t *testing.T) {
		svc, f := setup(t, "job-closed", api.WorkerMergeStatusMerged)
		testutil.FailErr(t, "drop journal", os.Remove(f.txPath()))
		testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
		if _, err := os.Lstat(promoteTransactionRetainedDir(f.txPath())); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inert slots survived: %v", err)
		}
	})
}

// rewriteDevice changes an identity's leading device number, as a reboot may.
func rewriteDevice(t *testing.T, identity string) string {
	t.Helper()
	device, rest, ok := strings.Cut(identity, ":")
	if !ok {
		t.Fatalf("identity %q has no device field", identity)
	}
	return "9" + device + ":" + rest
}

func TestPromoteRecoverySurvivesDeviceNumberChange(t *testing.T) {
	crashed := func(t *testing.T) *promoteCrashFixture {
		t.Helper()
		f := newPromoteCrashFixture(t)
		if err := f.run(t, 5, nil); !errors.Is(err, errPromoteCrash) {
			t.Fatalf("crash: %v", err)
		}
		journal, _, err := readPromoteTransaction(f.txPath())
		testutil.FailErr(t, "read journal", err)
		for i, mutation := range journal.Mutations {
			if mutation.OriginalRetention == promoteRetentionInode {
				journal.Mutations[i].OriginalIdentity = rewriteDevice(t, mutation.OriginalIdentity)
			}
		}
		testutil.FailErr(t, "rewrite journal", writePromoteTransaction(f.txPath(), journal))
		return f
	}
	t.Run("valid recovery proceeds and restores the inode", func(t *testing.T) {
		crashed(t).assertSettlesAndRetries(t)
	})
	t.Run("replaced slot refuses", func(t *testing.T) {
		f := crashed(t)
		testutil.FailErr(t, "remove slot", os.Remove(f.slot("edited.sh")))
		testutil.FailErr(t, "replace slot", os.WriteFile(f.slot("edited.sh"), []byte("old-edit\n"), 0o750))
		testutil.FailErr(t, "chmod slot", os.Chmod(f.slot("edited.sh"), 0o750))
		if _, err := recoverPromoteTransaction(f.dataDir, f.task.ID, f.promote, f.task); err == nil || !strings.Contains(err.Error(), "diverged") {
			t.Fatalf("recover = %v, want diverged", err)
		}
	})
	t.Run("replaced primary refuses", func(t *testing.T) {
		f := crashed(t)
		testutil.FailErr(t, "remove primary", os.Remove(f.edited))
		testutil.FailErr(t, "replace primary", os.WriteFile(f.edited, []byte("external\n"), 0o750))
		if _, err := recoverPromoteTransaction(f.dataDir, f.task.ID, f.promote, f.task); err == nil || !strings.Contains(err.Error(), "diverged") {
			t.Fatalf("recover = %v, want diverged", err)
		}
		if raw, err := os.ReadFile(f.edited); err != nil || string(raw) != "external\n" {
			t.Fatalf("refusal overwrote primary: %q %v", raw, err)
		}
	})
	t.Run("primary replaced with the original bytes relinks the retained inode", func(t *testing.T) {
		f := crashed(t)
		testutil.FailErr(t, "remove primary", os.Remove(f.edited))
		testutil.FailErr(t, "replace primary", os.WriteFile(f.edited, []byte("old-edit\n"), 0o750))
		testutil.FailErr(t, "chmod primary", os.Chmod(f.edited, 0o750))
		f.assertSettlesAndRetries(t)
	})
}
