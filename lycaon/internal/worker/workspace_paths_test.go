package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// The state folder is reached through a link, so the configured path and the
// one the filesystem resolves differ.
func TestEveryReaderReportsTheWorkspacePathsThatWereBound(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	testutil.FailErr(t, "link state folder", os.Symlink(t.TempDir(), state))
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(state, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	queue := NewSQLServices(sqlDB, 1).Queue
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
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE worker_jobs SET lease_expires_at = ? WHERE id = ?`, timelayout.Format(time.Now().Add(-time.Minute)), first.ID)
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

func TestWorkspaceOutsideTheInstallationIsNotBound(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	queue := NewSQLServices(sqlDB, 1).Queue
	testutil.FailErr(t, "insert", queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implement",
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusPending,
	}))
	inside := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", "worker-branch")
	baseline := testbaseline.Durable(t, sqlDB, "job-1", t.TempDir())
	elsewhere := t.TempDir()
	cases := map[string]struct {
		store          *SQLStore
		root, baseline string
		want           error
	}{
		"branch elsewhere":   {store: queue.store, root: filepath.Join(elsewhere, "worker-branches", "worker-branch"), baseline: baseline},
		"baseline elsewhere": {store: queue.store, root: inside, baseline: filepath.Join(elsewhere, "worker-baselines", filepath.Base(baseline))},
		"no data directory":  {store: NewSQLStoreIn(sqlDB, ""), root: inside, baseline: baseline, want: jobstate.ErrNoDataDir},
	}
	for name, tc := range cases {
		bound, err := tc.store.SetWorkerWorkspace(t.Context(), "job-1", tc.root, tc.baseline)
		if err == nil || bound {
			t.Errorf("%s: bound = %v, err = %v, want a refusal", name, bound, err)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		task, ok := queue.Get("job-1")
		if !ok || task.WorkspaceRoot != "" || task.WorkspaceBaselinePath != "" {
			t.Errorf("%s: job holds workspace %q with baseline %q after a refusal", name, task.WorkspaceRoot, task.WorkspaceBaselinePath)
		}
	}
}

func TestBoundWorkspaceIsNotReadWithoutADataDirectory(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	store := NewSQLStore(sqlDB)
	testutil.FailErr(t, "insert", store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implement",
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusPending,
	}))
	root := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", "worker-branch")
	_, err := store.SetWorkerWorkspace(t.Context(), "job-1", root, testbaseline.Durable(t, sqlDB, "job-1", t.TempDir()))
	testutil.FailErr(t, "bind worker workspace", err)
	if _, err := NewSQLStoreIn(sqlDB, "").List(t.Context(), testdbseed.DefaultProjectID); !errors.Is(err, jobstate.ErrNoDataDir) {
		t.Fatalf("list without a data directory: err = %v, want %v", err, jobstate.ErrNoDataDir)
	}
}
