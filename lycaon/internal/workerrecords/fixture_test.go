package workerrecords_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

type fixture struct {
	db    *db.Store
	store *workerrecords.Store
	coord workerrecords.Actor
	other workerrecords.Actor
}

const (
	coordSession = "coord-session"
	otherSession = "other-session"
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "workers.db")
	testdbseed.InsertSession(t, sqlDB, coordSession, testdbseed.DefaultProjectID)
	insertSession(t, sqlDB, otherSession, "")
	return &fixture{
		db:    sqlDB,
		store: workerrecords.New(sqlDB),
		coord: workerrecords.Actor{SessionID: coordSession, Role: workerrecords.RoleCoordinator, PrincipalID: coordSession},
		other: workerrecords.Actor{SessionID: otherSession, Role: workerrecords.RoleCoordinator, PrincipalID: otherSession},
	}
}

func stamp() string { return db.FormatTime(time.Now().UTC()) }

func insertSession(t *testing.T, sqlDB *db.Store, id, parent string) {
	t.Helper()
	now := stamp()
	var parentID any
	if parent != "" {
		parentID = parent
	}
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (
			id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at
		) VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', ?, ?, ?, ?)`,
		id, testdbseed.DefaultProjectID, parentID, now, now, now)
	testutil.FailErr(t, "insert session "+id, err)
}

// childActor is the worker side: the child session of coordSession.
func (f *fixture) childActor(t *testing.T, child string) workerrecords.Actor {
	t.Helper()
	insertSession(t, f.db, child, coordSession)
	return workerrecords.Actor{SessionID: child, Role: workerrecords.RoleWorker, PrincipalID: child}
}

func (f *fixture) insertJob(t *testing.T, id, parent, child, status string) {
	t.Helper()
	var childID any
	if child != "" {
		childID = child
	}
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (
			id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at
		) VALUES (?, ?, ?, ?, 'implementer', ?, 'prompt', 'brief', ?)`,
		id, testdbseed.DefaultProjectID, parent, childID, status, stamp())
	testutil.FailErr(t, "insert job "+id, err)
}

func (f *fixture) insertResult(t *testing.T, id, jobID string) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, id, jobID, stamp())
	testutil.FailErr(t, "insert result "+id, err)
}

func (f *fixture) insertLanded(t *testing.T, id, jobID string) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES (?, ?, '/repo', ?)`, id, jobID, stamp())
	testutil.FailErr(t, "insert landed change "+id, err)
}

func op(id string) workerrecords.Op { return workerrecords.Op{ID: id} }

func (f *fixture) createWorker(t *testing.T, opID string, spec workerrecords.WorkerSpec) workerrecords.WorkerReceipt {
	t.Helper()
	if spec.AgentType == "" {
		spec.AgentType = "implementer"
	}
	if spec.ScopeMode == "" {
		spec.ScopeMode = workerrecords.ScopeWrite
	}
	receipt, err := f.store.CreateWorker(t.Context(), f.coord, op(opID), spec)
	testutil.FailErr(t, "create worker", err)
	return receipt
}

// workerWithRound creates a worker on child and admits a round on a fresh job.
func (f *fixture) workerWithRound(t *testing.T, name, child, jobID string) (workerrecords.WorkerReceipt, workerrecords.Actor) {
	t.Helper()
	childActor := f.childActor(t, child)
	w := f.createWorker(t, "create-"+name, workerrecords.WorkerSpec{Name: name, ChildSessionID: child})
	f.insertJob(t, jobID, coordSession, child, "running")
	_, err := f.store.AdmitRound(t.Context(), f.coord, op("admit-"+jobID), w.WorkerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit round", err)
	return w, childActor
}

func outboxHook(topic string) func(context.Context, *sql.Tx, any) error {
	return func(ctx context.Context, tx *sql.Tx, _ any) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO event_outbox (event_id, topic, project_id, data_json, created_at) VALUES (?, ?, ?, '{}', ?)`,
			topic+"-"+stamp(), topic, testdbseed.DefaultProjectID, stamp())
		return err
	}
}

func countRows(t *testing.T, sqlDB *db.Store, query string, args ...any) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count", sqlDB.QueryRowContext(t.Context(), query, args...).Scan(&n))
	return n
}
