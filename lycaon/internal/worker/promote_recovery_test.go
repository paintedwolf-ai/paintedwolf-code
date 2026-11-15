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
		if _, err := os.Lstat(f.txDir()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction survived settlement: %v", err)
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
			if _, err := os.Lstat(f.journalPath()); err != nil {
				t.Fatalf("uncommitted journal removed: %v", err)
			}
			if _, err := os.Lstat(f.slot("edited.sh")); err != nil {
				t.Fatalf("uncommitted slot removed: %v", err)
			}
		})
	}
	t.Run("journal without a known job is kept", func(t *testing.T) {
		svc, f := setup(t, "job-known", api.WorkerMergeStatusMerged)
		foreign := filepath.Join(promoteTransactionDir(f.dataDir, "job-unknown"), "transaction.json")
		_, err := beginPromoteTransaction(f.dataDir, "job-unknown", nil)
		testutil.FailErr(t, "write foreign journal", err)
		testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
		if _, err := os.Lstat(foreign); err != nil {
			t.Fatalf("journal of an unknown job removed: %v", err)
		}
	})
	t.Run("journal copied from another job is kept", func(t *testing.T) {
		svc, f := setup(t, "job-merged", api.WorkerMergeStatusMerged)
		raw, err := os.ReadFile(f.journalPath())
		testutil.FailErr(t, "read journal", err)
		misnamed := filepath.Join(promoteTransactionDir(f.dataDir, "job-other"), "transaction.json")
		testutil.FailErr(t, "mkdir misnamed", os.MkdirAll(filepath.Dir(misnamed), 0o700))
		testutil.FailErr(t, "write misnamed journal", os.WriteFile(misnamed, raw, 0o600))
		testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
		if _, err := os.Lstat(misnamed); err != nil {
			t.Fatalf("misnamed journal removed: %v", err)
		}
	})
	t.Run("slots without a journal are inert", func(t *testing.T) {
		svc, f := setup(t, "job-closed", api.WorkerMergeStatusMerged)
		testutil.FailErr(t, "drop journal", os.Remove(f.journalPath()))
		testutil.FailErr(t, "recover", svc.RecoverOrphanedMergeApplies(ctx))
		if _, err := os.Lstat(f.txDir()); !errors.Is(err, os.ErrNotExist) {
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
		if err := f.run(t, 5, nil); err != nil {
			t.Fatalf("crash: %v", err)
		}
		document := f.readJournalDocument(t)
		for _, raw := range document["roots"].(map[string]any) {
			root := raw.(map[string]any)
			root["identity"] = rewriteDevice(t, root["identity"].(string))
		}
		for _, raw := range document["targets"].([]any) {
			before := raw.(map[string]any)["before"].(map[string]any)
			if group, ok := before["hard_link_group"].(string); ok && group != "" {
				before["hard_link_group"] = rewriteDevice(t, group)
			}
		}
		f.writeJournalDocument(t, document)
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
		if _, err := recoverPromoteTransaction(t.Context(), f.dataDir, f.task.ID, f.promote, f.task); err == nil {
			t.Fatal("recovery accepted a replaced entry")
		}
	})
	t.Run("replaced primary refuses", func(t *testing.T) {
		f := crashed(t)
		testutil.FailErr(t, "remove primary", os.Remove(f.edited))
		testutil.FailErr(t, "replace primary", os.WriteFile(f.edited, []byte("external\n"), 0o750))
		if _, err := recoverPromoteTransaction(t.Context(), f.dataDir, f.task.ID, f.promote, f.task); err == nil {
			t.Fatal("recovery accepted a replaced entry")
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
