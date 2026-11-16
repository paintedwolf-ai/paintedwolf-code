package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The state folder is reached through a link, so the configured path and the
// one the filesystem resolves differ.
func TestEveryReaderReportsTheWorkspacePathsThatWereBound(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	testutil.FailErr(t, "link state folder", os.Symlink(t.TempDir(), state))
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(state, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	queue := NewSQLQueue(sqlDB, 1)
	queue.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	testutil.FailErr(t, "insert", queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implement",
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusPending,
	}))
	claim := func() *api.WorkerTask {
		t.Helper()
		task, err := queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller"})
		testutil.FailErr(t, "claim", err)
		return task
	}
	first := claim()
	root := filepath.Join(state, "worker-branches", "worker-branch")
	baseline := testbaseline.Durable(t, sqlDB, first.ID, t.TempDir())
	bound, err := queue.store.SetWorkerWorkspace(t.Context(), first.ID, root, baseline)
	testutil.FailErr(t, "bind worker workspace", err)
	if !bound {
		t.Fatal("worker workspace was not bound")
	}
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE worker_jobs SET lease_expires_at = ? WHERE id = ?`, db.FormatTime(time.Now().Add(-time.Minute)), first.ID)
	testutil.FailErr(t, "expire claim", err)
	_, err = queue.RecoverExpiredClaims(t.Context())
	testutil.FailErr(t, "recover expired claim", err)

	readers := map[string]func() *api.WorkerTask{
		"claim in a transaction": claim,
		"get": func() *api.WorkerTask {
			task, ok := queue.Get(first.ID)
			if !ok {
				t.Fatal("job not found")
			}
			return task
		},
		"list": func() *api.WorkerTask {
			tasks, err := queue.store.List(t.Context(), testdbseed.DefaultProjectID)
			testutil.FailErr(t, "list", err)
			if len(tasks) != 1 {
				t.Fatalf("listed %d jobs, want 1", len(tasks))
			}
			return &tasks[0]
		},
	}
	for name, read := range readers {
		task := read()
		if task.WorkspaceRoot != root {
			t.Errorf("%s: workspace root = %q, want %q", name, task.WorkspaceRoot, root)
		}
		if task.WorkspaceBaselinePath != baseline {
			t.Errorf("%s: baseline path = %q, want %q", name, task.WorkspaceBaselinePath, baseline)
		}
	}
}
