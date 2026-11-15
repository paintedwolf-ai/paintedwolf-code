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
	f.source = workerroster.Source{Reader: workerops.Reader{Records: f.records, Jobs: f.jobs, Decisions: f.dec}, Touches: f.touches, Shown: workerroster.NewShownQuestions()}
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
	n, err := db.New(f.db).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(db.FormatTime(time.Now().UTC())), CheckpointID: checkpoint, JobID: jobID})
	if err == nil && n != 1 {
		t.Fatalf("settle %s touched %d rounds", jobID, n)
	}
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

// The board lists every open worker, beyond the request's row bound, with the
// same state, result and paths the roster carries.
func TestBoardListsEveryOpenWorker(t *testing.T) {
	f := newFixture(t)
	builder := f.worker(t, "builder", "implementer")
	f.admit(t, builder, "job-b1", api.WorkerStatusRunning)
	f.settle(t, builder, "job-b1", "checkpoint-b1", []string{"src/a.go"})
	for i := range workerroster.MaxRows {
		f.worker(t, fmt.Sprintf("idle%02d", i), "implementer")
	}
	rows, err := f.source.Board(t.Context(), "coord")
	testutil.FailErr(t, "board", err)
	if len(rows) != workerroster.MaxRows+1 {
		t.Fatalf("board rows = %d want %d", len(rows), workerroster.MaxRows+1)
	}
	b := rows[0]
	if b.Name != "builder" || b.State != api.WorkerStateDelivered || b.ResultHandle != "result-job-b1" ||
		b.ResultDisposition != api.WorkerResultOpen || !slices.Equal(b.ChangedPaths, []string{"src/a.go"}) {
		t.Fatalf("builder board row = %+v", b)
	}
}

// A question counts as shown once a request frame renders it, and only that
// question; it can be taken once.
func TestShownQuestionsFollowTheRenderedFrame(t *testing.T) {
	f := newFixture(t)
	asker := f.worker(t, "asker", "implementer")
	f.admit(t, asker, "job-a1", api.WorkerStatusRunning)
	question := api.WorkerDecisionRequest{WorkerID: "job-a1", Question: "Which parser?"}
	if f.source.Shown.Take("coord", "job-a1", question) {
		t.Fatal("a question was taken before any frame showed it")
	}
	f.dec["child-asker"] = question
	_, _, err := f.source.Template(t.Context(), "coord")
	testutil.FailErr(t, "render frame", err)
	changed := question
	changed.Question = "Which lexer?"
	if f.source.Shown.Take("coord", "job-a1", changed) {
		t.Fatal("a different question was taken")
	}
	if !f.source.Shown.Take("coord", "job-a1", question) {
		t.Fatal("the shown question was not taken")
	}
	if f.source.Shown.Take("coord", "job-a1", question) {
		t.Fatal("the question was taken twice")
	}
}

func TestRosterScaleRecencyWorkspaceStatesLabelsAndRoundPathScanBound(t *testing.T) {
	f := newFixture(t)
	// Seed read-scope, preparing, refreshed, reset, and long-history workers.
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('child-reader', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'repo-researcher', ?, ?, ?)`, testdbseed.DefaultProjectID, now(), now(), now())
	testutil.FailErr(t, "insert reader child", err)
	readerW, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-reader"}, workerrecords.WorkerSpec{
		Name: "reader", AgentType: "repo-researcher", ScopeMode: "read", Brief: "read repo", ChildSessionID: "child-reader",
	})
	testutil.FailErr(t, "create reader", err)
	f.admit(t, readerW.WorkerID, "job-reader-1", api.WorkerStatusRunning)

	prepW := f.worker(t, "prep-writer", "implementer")
	_ = prepW

	refreshedW := f.worker(t, "refreshed-writer", "implementer")
	f.admit(t, refreshedW, "job-ref-1", api.WorkerStatusRunning)
	f.settle(t, refreshedW, "job-ref-1", "cp-ref-1", []string{"pkg/ref.go"})
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES ('landed-ref-1', 'job-ref-1', '/repo', ?)`, now())
	testutil.FailErr(t, "insert landed ref", err)
	_, err = f.records.SetChange(t.Context(), f.coord, workerrecords.Op{ID: "land-ref"}, "result-job-ref-1", workerrecords.Change{State: workerrecords.ChangeLanded, LandedChangeID: "landed-ref-1"})
	testutil.FailErr(t, "land ref", err)
	_, err = f.records.RecordPreparation(t.Context(), f.coord, workerrecords.Op{ID: "prep-ref"}, refreshedW, workerrecords.Preparation{
		PreparationRef: "/lanes/ref", SourceResultID: "result-job-ref-1", BindingGeneration: 2,
	})
	testutil.FailErr(t, "record prep ref", err)

	resetW := f.worker(t, "reset-writer", "implementer")
	f.admit(t, resetW, "job-rst-1", api.WorkerStatusRunning)
	f.settle(t, resetW, "job-rst-1", "cp-rst-1", []string{"pkg/rst.go"})
	_, err = f.records.SetChange(t.Context(), f.coord, workerrecords.Op{ID: "rej-rst"}, "result-job-rst-1", workerrecords.Change{State: workerrecords.ChangeRejected})
	testutil.FailErr(t, "reject rst", err)

	// Long-history worker with 45 rounds; round 1 path is outside MaxRoundPathScan (32).
	longW := f.worker(t, "long-history", "implementer")
	for i := 1; i <= 45; i++ {
		jobID := fmt.Sprintf("job-lh-%02d", i)
		if i == 1 {
			f.admit(t, longW, jobID, api.WorkerStatusRunning)
			f.touches[jobID] = []string{"ancient/round1.go"}
		} else {
			_, jErr := f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
				VALUES (?, ?, 'coord', 'child-long-history', 'implementer', 'running', 'p', 'b', ?)`, jobID, testdbseed.DefaultProjectID, now())
			testutil.FailErr(t, "insert lh job", jErr)
			f.jobs[jobID] = &api.WorkerTask{ID: jobID, ParentSessionID: "coord", Status: api.WorkerStatusRunning}
			_, sErr := f.records.StartRound(t.Context(), f.coord, workerrecords.Op{ID: fmt.Sprintf("start-lh-%02d", i)}, longW, workerrecords.RoundStart{
				JobID: jobID, ExecutionTarget: "local", Instruction: fmt.Sprintf("lh %d", i),
			})
			testutil.FailErr(t, "start lh round", sErr)
			if i == 45 {
				f.touches[jobID] = []string{"recent/round45.go"}
			}
		}
		f.settle(t, longW, jobID, "", nil)
		if i < 45 {
			_, _ = f.records.AccountResult(t.Context(), f.coord, workerrecords.Op{ID: fmt.Sprintf("acct-lh-%02d", i)}, "result-"+jobID)
		}
	}

	// Add 45 idle workers with distinct updated_at timestamps to verify recency ordering within equal priority.
	for i := range 45 {
		name := fmt.Sprintf("idle-%02d", i)
		id := f.worker(t, name, "implementer")
		_, err := f.records.RecordPreparation(t.Context(), f.coord, workerrecords.Op{ID: "prep-" + name}, id, workerrecords.Preparation{
			PreparationRef: "/lanes/" + name, BindingGeneration: 1,
		})
		testutil.FailErr(t, "prep idle", err)
		ts := fmt.Sprintf("2026-11-15T12:%02d:00Z", i)
		_, err = f.db.ExecContext(t.Context(), `UPDATE workers SET updated_at = ? WHERE id = ?`, ts, id)
		testutil.FailErr(t, "stamp updated_at", err)
	}

	boardRows, err := f.source.Board(t.Context(), "coord")
	testutil.FailErr(t, "board 50 workers", err)
	if len(boardRows) != 50 {
		t.Fatalf("board rows = %d, want 50", len(boardRows))
	}

	byName := map[string]api.BoardWorker{}
	var idleOrder []string
	for _, r := range boardRows {
		byName[r.Name] = r
		if len(r.Name) > 5 && r.Name[:5] == "idle-" {
			idleOrder = append(idleOrder, r.Name)
		}
	}
	if idleOrder[0] != "idle-44" || idleOrder[len(idleOrder)-1] != "idle-00" {
		t.Fatalf("idle recency order = first:%s last:%s", idleOrder[0], idleOrder[len(idleOrder)-1])
	}
	if byName["reader"].WorkspaceState != "shared" || byName["reader"].WorkspaceLabel != "shared read view" {
		t.Fatalf("reader workspace = %+v", byName["reader"])
	}
	if byName["prep-writer"].WorkspaceState != "preparing" || byName["prep-writer"].WorkspaceLabel != "preparing workspace" {
		t.Fatalf("prep-writer workspace = %+v", byName["prep-writer"])
	}
	if byName["refreshed-writer"].WorkspaceState != "refreshed" || byName["refreshed-writer"].DispositionLabel != "changes landed" {
		t.Fatalf("refreshed-writer = %+v", byName["refreshed-writer"])
	}
	if byName["reset-writer"].WorkspaceState != "reset" || byName["reset-writer"].DispositionLabel != "changes rejected" {
		t.Fatalf("reset-writer = %+v", byName["reset-writer"])
	}
	lh := byName["long-history"]
	if slices.Contains(lh.ReadPaths, "ancient/round1.go") || !slices.Contains(lh.ReadPaths, "recent/round45.go") {
		t.Fatalf("long-history read paths = %v, want recent window only", lh.ReadPaths)
	}

	tmplRows, omitted, err := f.source.Template(t.Context(), "coord")
	testutil.FailErr(t, "template", err)
	if len(tmplRows) != workerroster.MaxRows || omitted != 50-workerroster.MaxRows {
		t.Fatalf("template rows=%d omitted=%d", len(tmplRows), omitted)
	}
	latestMap, err := f.source.LatestResults(t.Context(), "coord")
	testutil.FailErr(t, "latest results", err)
	if latestMap["job-lh-45"].Worker != "long-history" {
		t.Fatalf("latest results missing long-history: %+v", latestMap)
	}
}
