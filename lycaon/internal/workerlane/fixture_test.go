package workerlane_test

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

type roundFixture struct {
	db       *db.Store
	records  *workerrecords.Store
	ws       *fakeWorkspaces
	lanes    *workerlane.Service
	actor    workerrecords.Actor
	workerID string
}

// newRoundFixture has a worker with a prepared lane and one settled round on
// job-1 with result-1.
func newRoundFixture(t *testing.T) *roundFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "lane.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	now := timelayout.Format(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', ?, ?, ?)`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert child", err)
	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}
	w, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite, ChildSessionID: "child"})
	testutil.FailErr(t, "create worker", err)
	ws := &fakeWorkspaces{base: t.TempDir()}
	f := &roundFixture{db: sqlDB, records: records, ws: ws, lanes: workerlane.New(ws, records), actor: actor, workerID: w.WorkerID}
	// As in production: the round is admitted when its job is enqueued and
	// its lane prepared when the job is claimed, before it settles.
	f.round(t, "job-1", "", "")
	_, err = f.lanes.Admit(t.Context(), workerlane.Request{Actor: actor, Op: workerrecords.Op{ID: "admit-1"}, WorkerID: w.WorkerID, Facts: workerlane.Facts{Write: true}})
	testutil.FailErr(t, "prepare lane", err)
	f.settleRound(t, "job-1", "result-1", "cp-job-1")
	return f
}

// round admits a round on a new job. A result settles it, with checkpoint
// when the round made changes.
func (f *roundFixture) round(t *testing.T, jobID, resultID, checkpoint string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, 'coord', 'child', 'implementer', 'complete', 'p', 'b', ?)`, jobID, testdbseed.DefaultProjectID, now)
	testutil.FailErr(t, "insert job", err)
	_, err = f.records.AdmitRound(t.Context(), f.actor, workerrecords.Op{ID: "round-" + jobID}, f.workerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit round", err)
	if resultID != "" {
		f.settleRound(t, jobID, resultID, checkpoint)
	}
}

// settleRound records a result and settles its round as the completion
// transaction does.
func (f *roundFixture) settleRound(t *testing.T, jobID, resultID, checkpoint string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, resultID, jobID, now)
	testutil.FailErr(t, "insert result", err)
	n, err := db.New(f.db).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), CheckpointID: checkpoint, JobID: jobID})
	testutil.FailErr(t, "settle round", err)
	if n != 1 {
		t.Fatalf("settle %s touched %d rounds", jobID, n)
	}
}
