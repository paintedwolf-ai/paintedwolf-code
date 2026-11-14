package workerrecords_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

// More messages than a page: the page replays until a successful response
// commits it, reading history acknowledges nothing, and each direction is
// delivered on its own.
func TestDeliveryPagesReplayUntilCommitted(t *testing.T) {
	f := newFixture(t)
	w, child := f.workerWithRound(t, "fixer", "child-1", "job-1")
	total := workerrecords.MaxPageMessages + 6
	for i := range total {
		_, err := f.store.AppendMessage(t.Context(), f.coord, op("in-"+strconv.Itoa(i)), w.WorkerID, workerrecords.MessageSpec{Body: "instruction " + strconv.Itoa(i)})
		testutil.FailErr(t, "append instruction", err)
	}
	_, err := f.store.AppendMessage(t.Context(), child, op("out-1"), w.WorkerID, workerrecords.MessageSpec{Body: "question", NeedsReply: true})
	testutil.FailErr(t, "append worker message", err)

	first, err := f.store.OpenPage(t.Context(), child, w.WorkerID, 0)
	testutil.FailErr(t, "open page", err)
	if len(first.Messages) != workerrecords.MaxPageMessages || first.Messages[0].Seq != 1 {
		t.Fatalf("first page = %d messages from seq %d", len(first.Messages), first.Messages[0].Seq)
	}
	for _, m := range first.Messages {
		if m.RecipientRole != workerrecords.RoleWorker || m.SenderRole != workerrecords.RoleCoordinator {
			t.Fatalf("worker page carries %+v", m)
		}
	}
	// The provider request failed: the same page comes back.
	replay, err := f.store.OpenPage(t.Context(), child, w.WorkerID, 0)
	testutil.FailErr(t, "reopen page", err)
	if replay.ID != first.ID || len(replay.Messages) != len(first.Messages) {
		t.Fatalf("replayed page %s (%d) != %s (%d)", replay.ID, len(replay.Messages), first.ID, len(first.Messages))
	}
	_, err = f.store.History(t.Context(), child, w.WorkerID, 0, 10)
	testutil.FailErr(t, "history", err)
	err = f.store.CommitPage(t.Context(), child, w.WorkerID, 0, "not-the-page")
	wantErr(t, "stale commit", err, workerrecords.ErrPageStale)
	testutil.FailErr(t, "commit", f.store.CommitPage(t.Context(), child, w.WorkerID, 0, first.ID))
	testutil.FailErr(t, "commit again", f.store.CommitPage(t.Context(), child, w.WorkerID, 0, first.ID))

	second, err := f.store.OpenPage(t.Context(), child, w.WorkerID, 0)
	testutil.FailErr(t, "second page", err)
	if len(second.Messages) != 6 || second.ID == first.ID {
		t.Fatalf("second page = %d messages, id %s", len(second.Messages), second.ID)
	}
	// A new session generation is shown everything again.
	fresh, err := f.store.OpenPage(t.Context(), child, w.WorkerID, 1)
	testutil.FailErr(t, "next generation", err)
	if len(fresh.Messages) != workerrecords.MaxPageMessages || fresh.Messages[0].Seq != 1 {
		t.Fatalf("generation 1 page = %d from %d", len(fresh.Messages), fresh.Messages[0].Seq)
	}

	coordPage, err := f.store.OpenPage(t.Context(), f.coord, w.WorkerID, 0)
	testutil.FailErr(t, "coordinator page", err)
	if len(coordPage.Messages) != 1 || !coordPage.Messages[0].NeedsReply || coordPage.Messages[0].RoundOrdinal != 1 || coordPage.Messages[0].SenderID != "child-1" {
		t.Fatalf("coordinator page = %+v", coordPage.Messages)
	}
	_, err = f.db.ExecContext(t.Context(), `UPDATE worker_messages SET body = 'edited' WHERE seq = 1`)
	if err == nil {
		t.Fatal("message edited in place")
	}
	_, err = f.store.AppendMessage(t.Context(), f.coord, op("reply-bad"), w.WorkerID, workerrecords.MessageSpec{Body: "reply", ReplyToSeq: 999})
	wantErr(t, "reply to nothing", err, workerrecords.ErrReplyUnknown)
	_, err = f.store.AppendMessage(t.Context(), f.coord, op("reply"), w.WorkerID, workerrecords.MessageSpec{Body: "reply", ReplyToSeq: int64(total + 1)})
	testutil.FailErr(t, "reply", err)
}

func TestDispositionsSeparateAccountingFromChanges(t *testing.T) {
	f := newFixture(t)
	w, _ := f.workerWithRound(t, "fixer", "child-1", "job-1")
	f.insertResult(t, "result-1", "job-1")
	f.insertJob(t, "job-other", coordSession, "", "complete")
	f.insertLanded(t, "landed-other", "job-other")
	f.insertLanded(t, "landed-1", "job-1")

	ref, err := f.store.Result(t.Context(), f.coord, "result-1")
	testutil.FailErr(t, "resolve result", err)
	if ref.WorkerID != w.WorkerID || ref.JobID != "job-1" || ref.LandedChangeID != "landed-1" {
		t.Fatalf("result ref = %+v", ref)
	}
	_, err = f.store.Result(t.Context(), f.other, "result-1")
	wantErr(t, "another session's result", err, workerrecords.ErrResultNotFound)

	d, err := f.store.AccountResult(t.Context(), f.coord, op("account"), "result-1")
	testutil.FailErr(t, "account", err)
	if d.AccountedAt == "" || d.ChangeState != "" || d.WorkerID != w.WorkerID {
		t.Fatalf("accounting landed something: %+v", d)
	}
	_, err = f.store.AccountResult(t.Context(), f.coord, op("account-2"), "result-1")
	wantErr(t, "second accounting", err, workerrecords.ErrAlreadyAccounted)

	d, err = f.store.SetChange(t.Context(), f.coord, op("shelve"), "result-1", workerrecords.Change{State: workerrecords.ChangeShelved, ShelveReason: "waiting on a deploy"})
	testutil.FailErr(t, "shelve", err)
	if d.AccountedAt == "" || d.ChangeState != workerrecords.ChangeShelved {
		t.Fatalf("shelving changed accounting: %+v", d)
	}
	_, err = f.store.SetChange(t.Context(), f.coord, op("land-wrong"), "result-1", workerrecords.Change{State: workerrecords.ChangeLanded, LandedChangeID: "landed-other"})
	wantErr(t, "another job's receipt", err, workerrecords.ErrInvalidDisposition)
	d, err = f.store.SetChange(t.Context(), f.coord, op("land"), "result-1", workerrecords.Change{State: workerrecords.ChangeLanded, LandedChangeID: "landed-1"})
	testutil.FailErr(t, "land shelved changes", err)
	if d.ChangeState != workerrecords.ChangeLanded || d.LandedChangeID != "landed-1" || d.ShelveReason != "" {
		t.Fatalf("landed = %+v", d)
	}
	_, err = f.store.SetChange(t.Context(), f.coord, op("reject"), "result-1", workerrecords.Change{State: workerrecords.ChangeRejected})
	wantErr(t, "reject after landing", err, workerrecords.ErrDispositionFinal)
	_, err = f.db.ExecContext(t.Context(), `UPDATE worker_result_dispositions SET change_state = 'rejected', landed_change_id = NULL WHERE result_id = 'result-1'`)
	if err == nil {
		t.Fatal("landed disposition rewritten")
	}
	_, err = f.store.SetChange(t.Context(), f.coord, op("bad"), "result-1", workerrecords.Change{State: workerrecords.ChangeShelved})
	wantErr(t, "shelving without a reason", err, workerrecords.ErrInvalidDisposition)
}

func TestPrerequisitesResolveAndRefuse(t *testing.T) {
	f := newFixture(t)
	producer, _ := f.workerWithRound(t, "producer", "child-p", "job-p")
	f.insertResult(t, "result-p", "job-p")
	consumer := f.createWorker(t, "create-consumer", workerrecords.WorkerSpec{Name: "consumer"})
	third := f.createWorker(t, "create-third", workerrecords.WorkerSpec{Name: "third"})

	r, err := f.store.AddPrerequisites(t.Context(), f.coord, op("pre-1"), consumer.WorkerID, workerrecords.Prerequisites{AfterWorkers: []string{"producer", " producer "}, BaseResultID: "result-p"})
	testutil.FailErr(t, "add prerequisites", err)
	if len(r.ProducerIDs) != 1 || r.ProducerIDs[0] != producer.WorkerID || r.BaseResultID != "result-p" {
		t.Fatalf("receipt = %+v", r)
	}
	_, err = f.store.AddPrerequisites(t.Context(), f.coord, op("pre-self"), consumer.WorkerID, workerrecords.Prerequisites{AfterWorkers: []string{"consumer"}})
	wantErr(t, "self", err, workerrecords.ErrPrerequisiteSelf)
	_, err = f.store.AddPrerequisites(t.Context(), f.coord, op("pre-missing"), consumer.WorkerID, workerrecords.Prerequisites{AfterWorkers: []string{"nobody"}})
	wantErr(t, "missing", err, workerrecords.ErrPrerequisiteUnknown)
	_, err = f.store.AddPrerequisites(t.Context(), f.coord, op("pre-chain"), third.WorkerID, workerrecords.Prerequisites{AfterWorkers: []string{"consumer"}})
	testutil.FailErr(t, "chain", err)
	_, err = f.store.AddPrerequisites(t.Context(), f.coord, op("pre-cycle"), producer.WorkerID, workerrecords.Prerequisites{AfterWorkers: []string{"third"}})
	wantErr(t, "cycle", err, workerrecords.ErrPrerequisiteCycle)

	_, err = f.store.CreateWorker(t.Context(), f.other, op("other-w"), workerrecords.WorkerSpec{Name: "producer", AgentType: "implementer", ScopeMode: "write"})
	testutil.FailErr(t, "other session worker", err)
	foreign, err := f.store.Resolve(t.Context(), f.other, "producer")
	testutil.FailErr(t, "resolve other", err)
	_, err = f.store.AddPrerequisites(t.Context(), f.other, op("pre-foreign-result"), foreign.ID, workerrecords.Prerequisites{BaseResultID: "result-p"})
	wantErr(t, "another session's result", err, workerrecords.ErrResultNotFound)
}

func TestPreparationsAppendInOrder(t *testing.T) {
	f := newFixture(t)
	w, _ := f.workerWithRound(t, "fixer", "child-1", "job-1")
	f.insertResult(t, "result-1", "job-1")
	p1, err := f.store.RecordPreparation(t.Context(), f.coord, op("prep-1"), w.WorkerID, workerrecords.Preparation{SourceResultID: "result-1", PromotionRef: "promotion-1", PreparationRef: "prep-1", BindingGeneration: 2})
	testutil.FailErr(t, "record", err)
	p2, err := f.store.RecordPreparation(t.Context(), f.coord, op("prep-2"), w.WorkerID, workerrecords.Preparation{PreparationRef: "prep-2", BindingGeneration: 3})
	testutil.FailErr(t, "record second", err)
	if p1.Generation != 1 || p2.Generation != 2 {
		t.Fatalf("generations = %d, %d", p1.Generation, p2.Generation)
	}
	_, err = f.store.RecordPreparation(t.Context(), f.coord, op("prep-3"), w.WorkerID, workerrecords.Preparation{BindingGeneration: 1})
	wantErr(t, "binding going back", err, workerrecords.ErrInvalidPreparation)
	latest, ok, err := f.store.LatestPreparation(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "latest", err)
	if !ok || latest.Generation != 2 || latest.PreparationRef != "prep-2" || latest.BindingGeneration != 3 {
		t.Fatalf("latest = %+v ok=%v", latest, ok)
	}
	_, _, err = f.store.LatestPreparation(t.Context(), f.other, w.WorkerID)
	wantErr(t, "another session", err, workerrecords.ErrWorkerNotFound)
}

func TestClosedWorkerKeepsHistoryAndRefusesWork(t *testing.T) {
	f := newFixture(t)
	w, _ := f.workerWithRound(t, "fixer", "child-1", "job-1")
	_, err := f.store.SettleRound(t.Context(), f.coord, op("settle"), w.WorkerID, "job-1", "")
	testutil.FailErr(t, "settle", err)
	_, err = f.store.Close(t.Context(), f.coord, op("close"), w.WorkerID, "withdrawn")
	testutil.FailErr(t, "close", err)
	got, err := f.store.Worker(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "read closed", err)
	if got.ClosedReason != "withdrawn" {
		t.Fatalf("closed worker = %+v", got)
	}
	rounds, err := f.store.Rounds(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "rounds", err)
	if len(rounds) != 1 || rounds[0].SettledAt == "" || rounds[0].ResultID != "" {
		t.Fatalf("rounds = %+v", rounds)
	}
	f.insertJob(t, "job-2", coordSession, "child-1", "pending")
	_, err = f.store.AdmitRound(t.Context(), f.coord, op("admit-closed"), w.WorkerID, workerrecords.RoundSpec{JobID: "job-2", ExecutionTarget: "local"})
	if !errors.Is(err, workerrecords.ErrWorkerClosed) {
		t.Fatalf("admission on a closed worker = %v", err)
	}
}

// Open lane roots are each open worker's current lane; earlier generations
// and closed workers' lanes are not listed.
func TestOpenLaneRootsListCurrentLanesOfOpenWorkers(t *testing.T) {
	f := newFixture(t)
	open, _ := f.workerWithRound(t, "open", "child-1", "job-1")
	closed, _ := f.workerWithRound(t, "closed", "child-2", "job-2")
	f.settle(t, closed.WorkerID, "job-2")
	for i, ref := range []string{"/lanes/open.1", "/lanes/open.2"} {
		_, err := f.store.RecordPreparation(t.Context(), f.coord, op("prep-open-"+strconv.Itoa(i)), open.WorkerID, workerrecords.Preparation{PreparationRef: ref, BindingGeneration: int64(i + 1)})
		testutil.FailErr(t, "record open lane", err)
	}
	_, err := f.store.RecordPreparation(t.Context(), f.coord, op("prep-closed"), closed.WorkerID, workerrecords.Preparation{PreparationRef: "/lanes/closed.1", BindingGeneration: 1})
	testutil.FailErr(t, "record closed lane", err)
	_, err = f.store.Close(t.Context(), f.coord, op("close"), closed.WorkerID, "withdrawn")
	testutil.FailErr(t, "close", err)

	roots, err := f.store.OpenLaneRoots(t.Context())
	testutil.FailErr(t, "open lane roots", err)
	if !slices.Equal(roots, []string{"/lanes/open.2"}) {
		t.Fatalf("open lane roots = %v", roots)
	}
}
