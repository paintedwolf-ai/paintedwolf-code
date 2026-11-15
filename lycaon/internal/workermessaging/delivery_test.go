package workermessaging_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workermessaging"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type deliveryFixture struct {
	db       *db.Store
	records  *workerrecords.Store
	delivery *workermessaging.Delivery
	coord    workerrecords.Actor
	child    workerrecords.Actor
	workerID string
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "messages.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	now := db.FormatTime(time.Now().UTC())
	for _, id := range []string{"child", "bare-child"} {
		_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
			VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', ?, ?, ?)`, id, testdbseed.DefaultProjectID, now, now, now)
		testutil.FailErr(t, "insert child", err)
	}
	records := workerrecords.New(sqlDB)
	f := &deliveryFixture{
		db: sqlDB, records: records, delivery: &workermessaging.Delivery{Records: records},
		coord: workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"},
		child: workerrecords.Actor{SessionID: "child", Role: workerrecords.RoleWorker, PrincipalID: "child"},
	}
	w, err := records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{Name: "parser", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child"})
	testutil.FailErr(t, "create worker", err)
	f.workerID = w.WorkerID
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES ('job-1', ?, 'coord', 'child', 'implementer', 'running', 'p', 'b', ?)`, testdbseed.DefaultProjectID, now)
	testutil.FailErr(t, "insert job", err)
	_, err = records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit"}, w.WorkerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit round", err)
	return f
}

func (f *deliveryFixture) send(t *testing.T, from workerrecords.Actor, op, body string, needsReply bool) {
	t.Helper()
	_, err := f.records.AppendMessage(t.Context(), from, workerrecords.Op{ID: op}, f.workerID, workerrecords.MessageSpec{Body: body, NeedsReply: needsReply})
	testutil.FailErr(t, "send "+op, err)
}

var (
	coordSession = &api.Session{ID: "coord"}
	childSession = &api.Session{ID: "child", ParentSessionID: "coord"}
)

func TestEachSideGetsOnlyItsDirection(t *testing.T) {
	f := newDeliveryFixture(t)
	f.send(t, f.coord, "in-1", "also fix the caller", false)
	f.send(t, f.child, "out-1", "the encoding is the real defect", true)

	toWorker, err := f.delivery.PrepareMessages(t.Context(), childSession)
	testutil.FailErr(t, "worker page", err)
	if toWorker.Recipient != workermessaging.RecipientWorker || len(toWorker.Lines) != 1 || toWorker.Lines[0].Body != "also fix the caller" {
		t.Fatalf("worker page = %+v", toWorker)
	}
	toCoord, err := f.delivery.PrepareMessages(t.Context(), coordSession)
	testutil.FailErr(t, "coordinator page", err)
	if toCoord.Recipient != workermessaging.RecipientCoordinator || len(toCoord.Lines) != 1 ||
		toCoord.Lines[0].Worker != "parser" || !toCoord.Lines[0].NeedsReply {
		t.Fatalf("coordinator page = %+v", toCoord)
	}
}

// A failed request replays the page; a committed one is not shown again, even
// in a new context generation: the coordinator's cursor carries across them.
func TestPagesReplayUntilCommittedAndAfterCompaction(t *testing.T) {
	f := newDeliveryFixture(t)
	f.send(t, f.child, "out-1", "first", false)
	first, err := f.delivery.PrepareMessages(t.Context(), coordSession)
	testutil.FailErr(t, "prepare", err)
	replay, err := f.delivery.PrepareMessages(t.Context(), coordSession)
	testutil.FailErr(t, "replay", err)
	if len(replay.Pages) != 1 || replay.Pages[0].PageID != first.Pages[0].PageID || len(replay.Lines) != 1 {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	testutil.FailErr(t, "commit", f.delivery.CommitMessages(t.Context(), "coord", first))
	after, err := f.delivery.PrepareMessages(t.Context(), coordSession)
	testutil.FailErr(t, "after commit", err)
	if len(after.Lines) != 0 {
		t.Fatalf("committed message shown again: %+v", after)
	}
	compacted := &api.Session{ID: "coord", CompactionGeneration: 1}
	again, err := f.delivery.PrepareMessages(t.Context(), compacted)
	testutil.FailErr(t, "after compaction", err)
	if len(again.Lines) != 0 {
		t.Fatalf("new generation page = %+v", again)
	}
}

func TestCoordinatorPagesAreBoundedAcrossWorkers(t *testing.T) {
	f := newDeliveryFixture(t)
	for i := range 40 {
		f.send(t, f.child, "out-"+strconv.Itoa(i), "note "+strconv.Itoa(i), false)
	}
	page, err := f.delivery.PrepareMessages(t.Context(), coordSession)
	testutil.FailErr(t, "prepare", err)
	if len(page.Lines) != 32 || page.Lines[0].Seq != 1 || !page.More {
		t.Fatalf("one worker's page = %d lines from %d, more %v", len(page.Lines), page.Lines[0].Seq, page.More)
	}
}

// A child session with no worker record, such as a job-only child, gets no page.
func TestJobOnlyChildGetsNoPage(t *testing.T) {
	f := newDeliveryFixture(t)
	page, err := f.delivery.PrepareMessages(t.Context(), &api.Session{ID: "bare-child", ParentSessionID: "coord"})
	testutil.FailErr(t, "prepare", err)
	if len(page.Lines) != 0 || len(page.Pages) != 0 {
		t.Fatalf("job-only child page = %+v", page)
	}
}

func TestMultiWorkerSaturationFairnessByteBoundsAndStreamCursorIndependence(t *testing.T) {
	f := newDeliveryFixture(t)
	now := db.FormatTime(time.Now().UTC())
	addWorker := func(name, childID, jobID string) (string, workerrecords.Actor) {
		_, err := f.db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
			VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', ?, ?, ?)`, childID, testdbseed.DefaultProjectID, now, now, now)
		testutil.FailErr(t, "insert child "+childID, err)
		w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-" + name}, workerrecords.WorkerSpec{
			Name: name, AgentType: "implementer", ScopeMode: "write", ChildSessionID: childID,
		})
		testutil.FailErr(t, "create "+name, err)
		_, err = f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
			VALUES (?, ?, 'coord', ?, 'implementer', 'running', 'p', 'b', ?)`, jobID, testdbseed.DefaultProjectID, childID, now)
		testutil.FailErr(t, "insert job "+jobID, err)
		_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-" + jobID}, w.WorkerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
		testutil.FailErr(t, "admit "+jobID, err)
		return w.WorkerID, workerrecords.Actor{SessionID: childID, Role: workerrecords.RoleWorker, PrincipalID: childID}
	}

	wB, childB := addWorker("lexer", "child-b", "job-b")
	wC, childC := addWorker("codegen", "child-c", "job-c")

	// Parser queues 40 messages, lexer queues 3, codegen queues 3; parser must not starve lexer or codegen.
	for i := range 40 {
		f.send(t, f.child, fmt.Sprintf("p-%02d", i), fmt.Sprintf("parser note %d", i), false)
	}
	for i := range 3 {
		_, err := f.records.AppendMessage(t.Context(), childB, workerrecords.Op{ID: fmt.Sprintf("b-%02d", i)}, wB, workerrecords.MessageSpec{Body: fmt.Sprintf("lexer note %d", i)})
		testutil.FailErr(t, "lexer msg", err)
		_, err = f.records.AppendMessage(t.Context(), childC, workerrecords.Op{ID: fmt.Sprintf("c-%02d", i)}, wC, workerrecords.MessageSpec{Body: fmt.Sprintf("codegen note %d", i)})
		testutil.FailErr(t, "codegen msg", err)
	}

	// Findings store cursor is committed concurrently and must stay independent of worker messages.
	fStore := findings.NewSQLStore(f.db)
	for i := range 15 {
		_, err := fStore.Append(t.Context(), "coord", "parser", fmt.Sprintf("finding %d", i), "a.go", "")
		testutil.FailErr(t, "append finding", err)
	}
	testutil.FailErr(t, "commit finding delivery", fStore.CommitDelivery(t.Context(), "job-1", "resp-1", findings.Delivery{Cursor: 8}))

	page1, err := f.delivery.PrepareMessages(t.Context(), coordSession)
	testutil.FailErr(t, "prepare saturated page", err)
	byWorker := map[string]int{}
	for _, line := range page1.Lines {
		byWorker[line.Worker]++
	}
	if byWorker["parser"] == 0 || byWorker["lexer"] == 0 || byWorker["codegen"] == 0 || !page1.More {
		t.Fatalf("unfair multi-worker page: counts=%v more=%v", byWorker, page1.More)
	}
	testutil.FailErr(t, "commit page 1", f.delivery.CommitMessages(t.Context(), "coord", page1))

	// Findings cursor is untouched by message commits.
	fDel, err := fStore.Delivery(t.Context(), "job-1")
	testutil.FailErr(t, "read finding delivery", err)
	if fDel.Cursor != 8 {
		t.Fatalf("finding cursor mutated by message commit: %d", fDel.Cursor)
	}

	// Oversized worker messages respect byte cap on coordinator pages.
	wD, childD := addWorker("heavy", "child-d", "job-d")
	for i := range 4 {
		_, err := f.records.AppendMessage(t.Context(), childD, workerrecords.Op{ID: fmt.Sprintf("d-%d", i)}, wD, workerrecords.MessageSpec{Body: strings.Repeat("H", 7000)})
		testutil.FailErr(t, "heavy msg", err)
	}
	// Drain remaining parser messages first, then verify heavy page is byte-capped (< 4 messages).
	for range 3 {
		p, pErr := f.delivery.PrepareMessages(t.Context(), coordSession)
		testutil.FailErr(t, "drain page", pErr)
		if len(p.Lines) == 0 {
			break
		}
		heavyCount := 0
		for _, l := range p.Lines {
			if l.Worker == "heavy" {
				heavyCount++
			}
		}
		if heavyCount > 2 {
			t.Fatalf("byte cap did not bound heavy worker lines: got %d in one page", heavyCount)
		}
		testutil.FailErr(t, "commit drain", f.delivery.CommitMessages(t.Context(), "coord", p))
	}
}
