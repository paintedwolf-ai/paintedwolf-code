package boards

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workerruntime"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"time"
)

func TestResearchAndCatalogResolveAttachedRootsToTheirProject(t *testing.T) {
	database := testdbfixture.Open(t, "research-roots.db")
	registry := project.NewSQLRegistry(database)
	primary, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve primary fixture path", err)
	secondary, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve secondary fixture path", err)
	yes, no := true, false
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{
		{Path: secondary, IsPrimary: &no}, {Path: primary, IsPrimary: &yes},
	}})
	testutil.FailErr(t, "create two-root project", err)
	runtime := New(Dependencies{Storage: persistence.Runtime{Projects: registry}})
	paths, err := runtime.ProjectRootPaths(t.Context())
	testutil.FailErr(t, "resolve research roots", err)
	if len(paths) != 2 || paths[0] != primary || paths[1] != secondary {
		t.Fatalf("research root precedence=%v", paths)
	}
	for _, root := range p.Roots {
		dirtyPath := " " + filepath.Join(root.Path, ".") + " "
		id, err := runtime.projectIDForRoot(t.Context(), dirtyPath)
		testutil.FailErr(t, "resolve research project", err)
		identity, ok, err := runtime.repoCatalogRoot(t.Context(), dirtyPath)
		testutil.FailErr(t, "resolve catalog identity", err)
		if id != p.ID || !ok || identity.ProjectID != p.ID || identity.RootID != root.ID {
			t.Fatalf("attached root crossed identity: research=%q catalog=%+v,%v", id, identity, ok)
		}
	}
	unknown := t.TempDir()
	if id, err := runtime.projectIDForRoot(t.Context(), unknown); err != nil || id != "" {
		t.Fatalf("unknown root acquired research project:%q,%v", id, err)
	}
	if _, ok, err := runtime.repoCatalogRoot(t.Context(), unknown); err != nil || ok {
		t.Fatalf("unknown root acquired catalog identity:%v,%v", ok, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.ProjectRootPaths(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled research enumeration=%v", err)
	}
	if _, err := runtime.projectIDForRoot(canceled, primary); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled project resolution=%v", err)
	}
	if _, _, err := runtime.repoCatalogRoot(canceled, primary); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled catalog resolution=%v", err)
	}
}

func TestWorkerReaderChecksLatestSettledResultReadiness(t *testing.T) {
	database := testdbfixture.Open(t, "reader.db")
	testdbseed.InsertSession(t, database, "coord", testdbseed.DefaultProjectID)
	now := timelayout.Format(time.Now().UTC())
	_, err := database.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'implementer', ?, ?, ?)`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert child", err)
	records := workerrecords.New(database)
	queue := worker.NewSQLServices(database, 1).Queue
	rt := &Runtime{
		WorkerRecords: records,
		deps: Dependencies{
			Workers:  &workerruntime.Runtime{Queue: queue},
			Sessions: &sessions.Runtime{},
		},
	}
	reader := rt.WorkerReader()
	coord := workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}
	w, err := records.CreateWorker(t.Context(), coord, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{
		Name: "fixer", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child",
	})
	testutil.FailErr(t, "create worker", err)
	_, err = records.RecordPreparation(t.Context(), coord, workerrecords.Op{ID: "prep"}, w.WorkerID, workerrecords.Preparation{
		PreparationRef: filepath.Join(t.TempDir(), "lane"), BindingGeneration: 1,
	})
	testutil.FailErr(t, "record prep", err)
	ready, err := reader.Readiness(t.Context(), coord, w.WorkerID)
	testutil.FailErr(t, "readiness before round", err)
	if ready {
		t.Fatal("expected false before any settled round")
	}
	_, err = database.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, merge_status, prompt, brief, created_at)
		VALUES ('job-1', ?, 'coord', 'child', 'implementer', 'complete', 'rebasing', 'p', 'b', ?)`, testdbseed.DefaultProjectID, now)
	testutil.FailErr(t, "insert job", err)
	_, err = records.AdmitRound(t.Context(), coord, workerrecords.Op{ID: "admit"}, w.WorkerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('res-1', 'job-1', 'complete', ?)`, now)
	testutil.FailErr(t, "insert result", err)
	_, err = db.New(database).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), CheckpointID: "cp-1", JobID: "job-1"})
	testutil.FailErr(t, "settle", err)

	ready, err = reader.Readiness(t.Context(), coord, w.WorkerID)
	testutil.FailErr(t, "readiness with rebasing promotion", err)
	if !ready {
		t.Fatal("expected unsettled promotion to report recovery readiness")
	}
}
