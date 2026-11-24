package toolhost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

type reconciledProgramProgress struct {
	store   progress.Store
	closure *progressclosure.Service
}

func (p reconciledProgramProgress) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	snap, err := p.store.Snapshot(ctx, id)
	if err != nil {
		return progress.PrerequisiteState{}, err
	}
	return progress.PrerequisiteState{
		SessionID:      id,
		RootSessionID:  id,
		Missing:        snap.Missing(),
		OpenSteps:      snap.OpenStepCount(),
		Reconciliation: p.closure.Reconciliation(id),
	}, nil
}

func TestComposedMutationRequiresAcknowledgementOfKnownWorkerFinishes(t *testing.T) {
	fixture := newComposedProgramFixture(t)
	store := progress.NewSQLStore(fixture.Database, sessionstore.NewSQL(fixture.Database))
	progresstest.SeedRows(t, store, fixture.Session,
		progress.RowSpec{Label: "Deliver"},
		progress.RowSpec{Label: "Verify"},
	)

	records := workerrecords.New(fixture.Database)
	now := timelayout.Format(time.Now().UTC())
	testdbseed.InsertSession(t, fixture.Database, fixture.Session, testdbseed.DefaultProjectID)
	_, err := fixture.Database.ExecContext(t.Context(), `INSERT INTO sessions (
			id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at, root_session_id
		) VALUES ('child-1', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', ?, ?, ?, ?, ?)`,
		testdbseed.DefaultProjectID, fixture.Session, now, now, now, fixture.Session)
	testutil.FailErr(t, "insert child session", err)
	actor := workerrecords.Actor{SessionID: fixture.Session, Role: workerrecords.RoleCoordinator, PrincipalID: fixture.Session}
	w, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "create-fixer"}, workerrecords.WorkerSpec{
		Name: "fixer", AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite, ChildSessionID: "child-1",
	})
	testutil.FailErr(t, "create worker", err)
	_, err = fixture.Database.ExecContext(t.Context(), `INSERT INTO worker_jobs (
			id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at
		) VALUES ('worker-one', ?, ?, 'child-1', 'implementer', 'running', 'prompt', 'brief', ?)`,
		testdbseed.DefaultProjectID, fixture.Session, now)
	testutil.FailErr(t, "insert job", err)
	round, err := records.AdmitRound(t.Context(), actor, workerrecords.Op{ID: "admit-1"}, w.WorkerID, workerrecords.RoundSpec{
		JobID: "worker-one", ExecutionTarget: "local",
	})
	testutil.FailErr(t, "admit round", err)
	handle := fmt.Sprintf("fixer#%d", round.Ordinal)
	_, err = fixture.Database.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, 'worker-one', 'complete', ?)`,
		handle, now)
	testutil.FailErr(t, "insert result", err)
	_, err = db.New(fixture.Database).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
		SettledAt: db.NullString(now), CheckpointID: "", JobID: "worker-one",
	})
	testutil.FailErr(t, "settle round", err)

	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.SetAccounts(records)
	closure.SetNames(records)
	closure.Arm(t.Context(), fixture.Session, "worker-one")
	fixture.Runtime.Progress = reconciledProgramProgress{store, closure}

	outcome := callComposedProgram(t, fixture, "printf 'payload\\n' > result.txt")
	if outcome.Refusal == nil || outcome.Refusal.Code != "PROGRESS_RECONCILIATION_REQUIRED" {
		t.Fatalf("pending finish admitted mutation: %+v", outcome)
	}
	if _, err := os.Stat(filepath.Join(fixture.Dir, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked program published a file: %v", err)
	}
	readonly := callComposedProgram(t, fixture, "printf 'inspection\\n'")
	if readonly.Status != 0 || readonly.Stdout != "inspection\n" {
		t.Fatalf("finish obligation blocked read-only work: %+v", readonly)
	}

	result, err := store.Apply(t.Context(), fixture.Session, progress.Mutation{
		AcknowledgeResults: []string{handle},
	}, progress.ApplyOptions{PrincipalID: fixture.Session, Reconciler: closure})
	testutil.FailErr(t, "acknowledge reviewed finish", err)
	if result.ReconciliationStatus != "acknowledged" || result.Updated {
		t.Fatalf("acknowledgement rewrote pending work: %+v", result)
	}
	outcome = callComposedProgram(t, fixture, "printf 'payload\\n' > result.txt")
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("acknowledged mutation refused: %+v", outcome)
	}
	contents, err := os.ReadFile(filepath.Join(fixture.Dir, "result.txt"))
	testutil.FailErr(t, "read committed native write", err)
	if string(contents) != "payload\n" {
		t.Fatalf("native write contents: %q", contents)
	}
}
