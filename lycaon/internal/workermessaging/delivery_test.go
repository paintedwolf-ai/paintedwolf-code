package workermessaging_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workermessaging"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type deliveryFixture struct {
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
		records: records, delivery: &workermessaging.Delivery{Records: records},
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

// A failed request replays the page; a committed one is not shown again, and
// a new context generation is shown everything again.
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
	if len(again.Lines) != 1 {
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
	if len(page.Lines) != 40 || page.Lines[0].Seq != 1 {
		t.Fatalf("one worker's page = %d lines from %d", len(page.Lines), page.Lines[0].Seq)
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
