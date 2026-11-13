package workerroster_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerroster"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type jobs map[string]*api.WorkerTask

func (j jobs) Get(id string) (*api.WorkerTask, bool) { t, ok := j[id]; return t, ok }

type decisions map[string]api.WorkerDecisionRequest

func (d decisions) Get(_ context.Context, child string) (api.WorkerDecisionRequest, bool, error) {
	r, ok := d[child]
	return r, ok, nil
}

type touches map[string][]string

func (t touches) Paths(jobID string) []string { return t[jobID] }

type fixture struct {
	db      *db.Store
	records *workerrecords.Store
	jobs    jobs
	dec     decisions
	touches touches
	coord   workerrecords.Actor
	source  workerroster.Source
}

func now() string { return db.FormatTime(time.Now().UTC()) }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "roster.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	f := &fixture{db: sqlDB, records: workerrecords.New(sqlDB), jobs: jobs{}, dec: decisions{}, touches: touches{},
		coord: workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}}
	f.source = workerroster.Source{Reader: workerops.Reader{Records: f.records, Jobs: f.jobs, Decisions: f.dec}, Touches: f.touches}
	return f
}

// worker creates a worker with its own child session.
func (f *fixture) worker(t *testing.T, name, agent string) string {
	t.Helper()
	child := "child-" + name
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', ?, ?, ?, ?)`, child, testdbseed.DefaultProjectID, agent, now(), now(), now())
	testutil.FailErr(t, "insert child", err)
	w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-" + name}, workerrecords.WorkerSpec{
		Name: name, AgentType: agent, ScopeMode: "write", Brief: "brief of " + name, ChildSessionID: child,
	})
	testutil.FailErr(t, "create worker", err)
	return w.WorkerID
}

func (f *fixture) admit(t *testing.T, workerID, jobID string, status api.WorkerStatus) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, 'coord', (SELECT child_session_id FROM workers WHERE id = ?), 'implementer', ?, 'p', 'b', ?)`, jobID, testdbseed.DefaultProjectID, workerID, string(status), now())
	testutil.FailErr(t, "insert job", err)
	f.jobs[jobID] = &api.WorkerTask{ID: jobID, ParentSessionID: "coord", Status: status}
	_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-" + jobID}, workerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
}

func (f *fixture) settle(t *testing.T, workerID, jobID, checkpoint string, changed []string) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, "result-"+jobID, jobID, now())
	testutil.FailErr(t, "insert result", err)
	_, err = f.records.SettleRound(t.Context(), f.coord, workerrecords.Op{ID: "settle-" + jobID}, workerID, jobID, checkpoint)
	testutil.FailErr(t, "settle", err)
	f.jobs[jobID] = &api.WorkerTask{ID: jobID, ParentSessionID: "coord", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
		ChangeReport:     &api.WorkerChangeReport{ChangedPaths: changed},
	}}
}

func rowByName(t *testing.T, r workerroster.Roster, name string) workerroster.Row {
	t.Helper()
	for _, row := range r.Rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("roster has no %s: %+v", name, r.Rows)
	return workerroster.Row{}
}

// Each row carries the derived state, the result handle and disposition, the
// pending question, and the paths read and changed across the worker's rounds.
func TestRosterRowsCarryStateResultQuestionAndPaths(t *testing.T) {
	f := newFixture(t)
	builder := f.worker(t, "builder", "implementer")
	f.admit(t, builder, "job-b1", api.WorkerStatusRunning)
	f.touches["job-b1"] = []string{"src/a.go", "src/b.go"}
	f.settle(t, builder, "job-b1", "checkpoint-b1", []string{"src/a.go"})

	asker := f.worker(t, "asker", "implementer")
	f.admit(t, asker, "job-a1", api.WorkerStatusRunning)
	f.dec["child-asker"] = api.WorkerDecisionRequest{WorkerID: "job-a1", Question: "Which parser?"}

	roster, err := f.source.Build(t.Context(), "coord")
	testutil.FailErr(t, "build roster", err)

	b := rowByName(t, roster, "builder")
	if b.State != workerstate.Delivered || b.Result == nil || b.Result.Handle != "result-job-b1" || b.Result.Disposition != "open" {
		t.Fatalf("builder row = %+v", b)
	}
	if !slices.Equal(b.Changed.Shown, []string{"src/a.go"}) || !slices.Equal(b.Read.Shown, []string{"src/a.go", "src/b.go"}) {
		t.Fatalf("builder paths = read %v changed %v", b.Read, b.Changed)
	}
	if b.About != "brief of builder" {
		t.Fatalf("builder about = %q", b.About)
	}
	a := rowByName(t, roster, "asker")
	if a.State != workerstate.Asking || a.Question != "Which parser?" {
		t.Fatalf("asker row = %+v", a)
	}
	if roster.Rows[0].Name != "asker" {
		t.Fatalf("an asking worker comes first: %+v", roster.Rows)
	}
}

// A shelved result shows its disposition; closed workers are left out.
func TestRosterShowsDispositionsAndLeavesOutClosedWorkers(t *testing.T) {
	f := newFixture(t)
	keeper := f.worker(t, "keeper", "implementer")
	f.admit(t, keeper, "job-k1", api.WorkerStatusRunning)
	f.settle(t, keeper, "job-k1", "checkpoint-k1", nil)
	_, err := f.records.SetChange(t.Context(), f.coord, workerrecords.Op{ID: "shelve"}, "result-job-k1", workerrecords.Change{State: workerrecords.ChangeShelved, ShelveReason: "later"})
	testutil.FailErr(t, "shelve", err)
	gone := f.worker(t, "gone", "implementer")
	_, err = f.records.Close(t.Context(), f.coord, workerrecords.Op{ID: "close"}, gone, "withdrawn")
	testutil.FailErr(t, "close", err)

	roster, err := f.source.Build(t.Context(), "coord")
	testutil.FailErr(t, "build roster", err)
	if k := rowByName(t, roster, "keeper"); k.Result == nil || k.Result.Disposition != workerrecords.ChangeShelved {
		t.Fatalf("keeper row = %+v", k)
	}
	for _, row := range roster.Rows {
		if row.Name == "gone" {
			t.Fatalf("closed worker listed: %+v", roster.Rows)
		}
	}
}

// The roster keeps MaxRows rows, counts the rest, and bounds each path list.
func TestRosterIsBounded(t *testing.T) {
	f := newFixture(t)
	var first string
	for i := range workerroster.MaxRows + 3 {
		id := f.worker(t, fmt.Sprintf("w%02d", i), "implementer")
		if first == "" {
			first = id
		}
	}
	f.admit(t, first, "job-many", api.WorkerStatusRunning)
	for i := range workerroster.MaxPaths + 4 {
		f.touches["job-many"] = append(f.touches["job-many"], fmt.Sprintf("p%02d", i))
	}
	roster, err := f.source.Build(t.Context(), "coord")
	testutil.FailErr(t, "build roster", err)
	if len(roster.Rows) != workerroster.MaxRows || roster.Omitted != 3 {
		t.Fatalf("rows=%d omitted=%d", len(roster.Rows), roster.Omitted)
	}
	w := rowByName(t, roster, "w00")
	if len(w.Read.Shown) != workerroster.MaxPaths || w.Read.Omitted != 4 {
		t.Fatalf("read paths = %+v", w.Read)
	}
}

// A settled round's notice names its worker, result handle, report status,
// changed paths, and the instructions that missed the round.
func TestResultNoticeDescribesTheSettledRound(t *testing.T) {
	f := newFixture(t)
	fixer := f.worker(t, "fixer", "implementer")
	f.admit(t, fixer, "job-f1", api.WorkerStatusRunning)
	_, err := f.records.AppendMessage(t.Context(), f.coord, workerrecords.Op{ID: "late"}, fixer, workerrecords.MessageSpec{Body: "check the encoder too"})
	testutil.FailErr(t, "queue instruction", err)
	f.settle(t, fixer, "job-f1", "checkpoint-f1", []string{"src/enc.go"})

	notice, ok, err := f.source.ResultNotice(t.Context(), "job-f1")
	testutil.FailErr(t, "result notice", err)
	if !ok || notice["worker"] != "fixer" || notice["result_handle"] != "result-job-f1" || notice["leg_status"] != "complete" {
		t.Fatalf("notice = %+v", notice)
	}
	if !slices.Equal(notice["changed"].([]string), []string{"src/enc.go"}) || !slices.Equal(notice["not_applied"].([]string), []string{"check the encoder too"}) {
		t.Fatalf("notice paths/instructions = %+v", notice)
	}
	if _, ok, err := f.source.ResultNotice(t.Context(), "job-unknown"); ok || err != nil {
		t.Fatalf("job without a round: ok=%v err=%v", ok, err)
	}
}
