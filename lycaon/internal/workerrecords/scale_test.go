package workerrecords_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

func TestLongHistoryPagingFrameBoundsAndRevisionCache(t *testing.T) {
	f := newFixture(t)
	child := f.childActor(t, "child-long")
	w := f.createWorker(t, "create-long", workerrecords.WorkerSpec{
		Name:           "long-runner",
		ChildSessionID: "child-long",
		Brief:          "Maintain parser invariants",
		BlueprintPath:  "plans/parser.md",
		WorkflowWorkID: "work-parser",
	})

	const settledRounds = 120
	seedSettledRounds(t, f, w.WorkerID, "child-long", settledRounds)

	f.insertLanded(t, "landed-120", "job-120")
	_, err := f.store.SetChange(t.Context(), f.coord, op("land-120"), "result-120", workerrecords.Change{State: workerrecords.ChangeLanded, LandedChangeID: "landed-120"})
	testutil.FailErr(t, "land 120", err)

	latestRes, latestDisp, hasLatest, err := f.store.LatestSettledResult(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "latest settled result", err)
	if !hasLatest || latestRes.Ordinal != settledRounds || latestRes.ResultID != "result-120" || latestDisp.ChangeState != workerrecords.ChangeLanded {
		t.Fatalf("latest settled = %+v disp=%+v has=%v", latestRes, latestDisp, hasLatest)
	}

	prov, hasProv, err := f.store.RoundProvenanceByJob(t.Context(), "job-120")
	testutil.FailErr(t, "provenance by job", err)
	if !hasProv || prov.Worker != "long-runner" || prov.Ordinal != 120 || prov.WorkflowWorkID != "work-parser" {
		t.Fatalf("provenance = %+v has=%v", prov, hasProv)
	}

	// Start round 121 and append 80 instructions including multi-kilobyte bodies.
	f.insertJob(t, "job-121", coordSession, "child-long", "pending")
	_, err = f.store.StartRound(t.Context(), f.coord, op("start-121"), w.WorkerID, workerrecords.RoundStart{
		JobID: "job-121", ExecutionTarget: "local", Instruction: "round 121 anchor instruction",
	})
	testutil.FailErr(t, "start 121", err)
	_, err = f.store.RecordPreparation(t.Context(), f.coord, op("prep-121"), w.WorkerID, workerrecords.Preparation{
		PreparationRef: "/lanes/long-runner.121", BindingGeneration: 2,
	})
	testutil.FailErr(t, "prep 121", err)

	for i := range 80 {
		body := fmt.Sprintf("steer-%02d: %s", i, strings.Repeat("x", 512))
		_, err := f.store.AppendMessage(t.Context(), f.coord, op(fmt.Sprintf("steer-121-%02d", i)), w.WorkerID, workerrecords.MessageSpec{Body: body})
		testutil.FailErr(t, "append steer", err)
	}
	// Worker sends unread messages including one oversized message (> MaxHistoryPageBytes).
	_, err = f.store.AppendMessage(t.Context(), child, op("worker-note-1"), w.WorkerID, workerrecords.MessageSpec{Body: "small unread note"})
	testutil.FailErr(t, "worker note 1", err)
	_, err = f.store.AppendMessage(t.Context(), child, op("worker-note-big"), w.WorkerID, workerrecords.MessageSpec{Body: strings.Repeat("B", workerrecords.MaxHistoryPageBytes+2048)})
	testutil.FailErr(t, "worker note big", err)

	summary, err := f.store.ReadStateSummary(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "read state summary", err)
	if !summary.HasRounds || !summary.HasSettledRound || summary.LatestRound == nil || summary.LatestRound.Ordinal != 121 || len(summary.UnaccountedJobs) != 1 || summary.UnaccountedJobs[0] != "job-120" {
		t.Fatalf("state summary = %+v", summary)
	}

	// Page all 121 rounds 20 at a time via RoundsBefore.
	var pagedOrdinals []int64
	beforeOrd := int64(0)
	roundPages := 0
	for {
		rows, rErr := f.store.RoundsBefore(t.Context(), f.coord, w.WorkerID, beforeOrd, workerrecords.MaxPageRounds+1)
		testutil.FailErr(t, "rounds before", rErr)
		hasMore := len(rows) > workerrecords.MaxPageRounds
		if hasMore {
			rows = rows[:workerrecords.MaxPageRounds]
		}
		for _, r := range rows {
			pagedOrdinals = append(pagedOrdinals, r.Ordinal)
		}
		roundPages++
		if !hasMore || len(rows) == 0 {
			break
		}
		beforeOrd = rows[len(rows)-1].Ordinal
	}
	if len(pagedOrdinals) != 121 || pagedOrdinals[0] != 121 || pagedOrdinals[120] != 1 || roundPages != 7 {
		t.Fatalf("paged rounds count=%d pages=%d first=%d last=%d", len(pagedOrdinals), roundPages, pagedOrdinals[0], pagedOrdinals[len(pagedOrdinals)-1])
	}

	// HistoryBounded enforces byte limits, returns oversized single messages, and never advances unread cursors.
	deliveriesBefore := countRows(t, f.db, `SELECT COUNT(*) FROM worker_message_deliveries WHERE worker_id = ?`, w.WorkerID)
	hist1, next1, err := f.store.HistoryBounded(t.Context(), f.coord, w.WorkerID, 0, 50, workerrecords.MaxHistoryPageBytes)
	testutil.FailErr(t, "history bounded 1", err)
	if len(hist1) != 1 || next1 == 0 || len(hist1[0].Body) <= workerrecords.MaxHistoryPageBytes {
		t.Fatalf("oversized history page 1: len=%d next=%d", len(hist1), next1)
	}
	hist2, next2, err := f.store.HistoryBounded(t.Context(), f.coord, w.WorkerID, next1, 50, workerrecords.MaxHistoryPageBytes)
	testutil.FailErr(t, "history bounded 2", err)
	if len(hist2) == 0 || len(hist2) >= 50 || next2 == 0 {
		t.Fatalf("byte-capped history page 2: len=%d next=%d", len(hist2), next2)
	}
	deliveriesAfter := countRows(t, f.db, `SELECT COUNT(*) FROM worker_message_deliveries WHERE worker_id = ?`, w.WorkerID)
	if deliveriesAfter != deliveriesBefore {
		t.Fatalf("HistoryBounded mutated deliveries: before=%d after=%d", deliveriesBefore, deliveriesAfter)
	}

	// Verify RoundFrame bounds and revision cache hit/invalidation.
	hits0, misses0 := f.store.RoundFrameCacheStats()
	frame1, ok, err := f.store.RoundFrame(t.Context(), child)
	testutil.FailErr(t, "round frame 1", err)
	if !ok || frame1.Ordinal != 121 || frame1.Workspace != workerrecords.WorkspaceRefreshed || frame1.PriorChange != workerrecords.ChangeLanded {
		t.Fatalf("frame1 = %+v ok=%v", frame1, ok)
	}
	if len(frame1.EarlierResults) != workerrecords.MaxFrameEarlierResults || frame1.EarlierResults[0] != "result-057" || frame1.EarlierResults[63] != "result-120" {
		t.Fatalf("earlier results len=%d first=%q last=%q", len(frame1.EarlierResults), frame1.EarlierResults[0], frame1.EarlierResults[len(frame1.EarlierResults)-1])
	}
	if len(frame1.Instructions) == 0 || len(frame1.Instructions) > workerrecords.MaxFrameInstructions || frame1.Instructions[0].Body != "round 121 anchor instruction" {
		t.Fatalf("instructions bounded len=%d first=%q", len(frame1.Instructions), frame1.Instructions[0].Body)
	}
	hits1, misses1 := f.store.RoundFrameCacheStats()
	if misses1 != misses0+1 || hits1 != hits0 {
		t.Fatalf("expected cache miss: hits %d->%d misses %d->%d", hits0, hits1, misses0, misses1)
	}

	frame2, ok, err := f.store.RoundFrame(t.Context(), child)
	testutil.FailErr(t, "round frame 2", err)
	hits2, misses2 := f.store.RoundFrameCacheStats()
	if !ok || hits2 != hits1+1 || misses2 != misses1 || len(frame2.Instructions) != len(frame1.Instructions) {
		t.Fatalf("expected cache hit: hits %d->%d misses %d->%d", hits1, hits2, misses1, misses2)
	}

	// Appending a new instruction invalidates the revision key immediately.
	_, err = f.store.AppendMessage(t.Context(), f.coord, op("steer-121-new"), w.WorkerID, workerrecords.MessageSpec{Body: "final tweak"})
	testutil.FailErr(t, "append invalidating steer", err)
	frame3, ok, err := f.store.RoundFrame(t.Context(), child)
	testutil.FailErr(t, "round frame 3", err)
	_, misses3 := f.store.RoundFrameCacheStats()
	if !ok || misses3 != misses2+1 || frame3.Instructions[len(frame3.Instructions)-1].Body != "final tweak" {
		t.Fatalf("cache did not invalidate on new instruction: misses %d->%d last=%q", misses2, misses3, frame3.Instructions[len(frame3.Instructions)-1].Body)
	}

	// Settling the round evicts the entry and EarlierResults stays bounded to MaxFrameEarlierResults.
	f.insertResult(t, "result-121", "job-121")
	f.settle(t, "job-121", "")
	_, ok, err = f.store.RoundFrame(t.Context(), child)
	testutil.FailErr(t, "round frame after settle", err)
	if ok || f.store.CachedRoundFrameCount() != 0 {
		t.Fatalf("settled worker frame ok=%v cached=%d", ok, f.store.CachedRoundFrameCount())
	}
	earlier, err := f.store.EarlierResults(t.Context(), child)
	testutil.FailErr(t, "earlier results after settle", err)
	if len(earlier) != workerrecords.MaxFrameEarlierResults || earlier[len(earlier)-1] != "result-121" {
		t.Fatalf("earlier results = len %d, last %q", len(earlier), earlier[len(earlier)-1])
	}
}

func seedSettledRounds(t *testing.T, f *fixture, workerID, childID string, settledRounds int) {
	t.Helper()
	for i := 1; i <= settledRounds; i++ {
		jobID := fmt.Sprintf("job-%03d", i)
		resID := fmt.Sprintf("result-%03d", i)
		f.insertJob(t, jobID, coordSession, childID, "pending")
		if i == 1 {
			_, err := f.store.AdmitRound(t.Context(), f.coord, op(fmt.Sprintf("admit-%03d", i)), workerID, workerrecords.RoundSpec{
				JobID: jobID, ExecutionTarget: "local",
			})
			testutil.FailErr(t, "admit round 1", err)
		} else {
			_, err := f.store.StartRound(t.Context(), f.coord, op(fmt.Sprintf("start-%03d", i)), workerID, workerrecords.RoundStart{
				JobID: jobID, ExecutionTarget: "local", Instruction: fmt.Sprintf("round %d goal", i),
			})
			testutil.FailErr(t, "start round", err)
		}
		f.insertResult(t, resID, jobID)
		cp := ""
		if i == settledRounds {
			cp = "cp-120"
		}
		f.settle(t, jobID, cp)
		if i < settledRounds {
			_, err := f.store.AccountResult(t.Context(), f.coord, op(fmt.Sprintf("acct-%03d", i)), resID)
			testutil.FailErr(t, "account result", err)
		}
	}
}

func TestRoundFrameCacheResidentEntriesStayBounded(t *testing.T) {
	f := newFixture(t)
	for i := range 80 {
		name := fmt.Sprintf("w-%02d", i)
		childID := fmt.Sprintf("child-%02d", i)
		jobID := fmt.Sprintf("job-%02d", i)
		_, child := f.workerWithRound(t, name, childID, jobID)
		_, ok, err := f.store.RoundFrame(t.Context(), child)
		testutil.FailErr(t, "round frame", err)
		if !ok {
			t.Fatalf("worker %d missing frame", i)
		}
	}
	if got := f.store.CachedRoundFrameCount(); got != workerrecords.MaxCachedRoundFrames {
		t.Fatalf("resident cached frames = %d, want %d", got, workerrecords.MaxCachedRoundFrames)
	}
}

func TestPendingCoordinatorMessageWorkersOrdersByOldestUnreadAndBoundsBytes(t *testing.T) {
	f := newFixture(t)
	wA, childA := f.workerWithRound(t, "alpha", "child-a", "job-a")
	wB, childB := f.workerWithRound(t, "beta", "child-b", "job-b")

	_, err := f.store.AppendMessage(t.Context(), childB, op("b-1"), wB.WorkerID, workerrecords.MessageSpec{Body: strings.Repeat("b", 9000)})
	testutil.FailErr(t, "b-1", err)
	_, err = f.store.AppendMessage(t.Context(), childB, op("b-2"), wB.WorkerID, workerrecords.MessageSpec{Body: strings.Repeat("c", 9000)})
	testutil.FailErr(t, "b-2", err)
	_, err = f.store.AppendMessage(t.Context(), childA, op("a-1"), wA.WorkerID, workerrecords.MessageSpec{Body: "from alpha"})
	testutil.FailErr(t, "a-1", err)

	pending, err := f.store.PendingCoordinatorMessageWorkers(t.Context(), f.coord)
	testutil.FailErr(t, "pending workers", err)
	if len(pending) != 2 || pending[0].ID != wB.WorkerID || pending[1].ID != wA.WorkerID {
		t.Fatalf("pending order = %+v, want [%s %s]", pending, wB.WorkerID, wA.WorkerID)
	}

	page1, err := f.store.OpenPageBounded(t.Context(), f.coord, wB.WorkerID, 0, 10, 12000)
	testutil.FailErr(t, "open bounded page 1", err)
	if len(page1.Messages) != 1 || !page1.More {
		t.Fatalf("page1 len=%d more=%v", len(page1.Messages), page1.More)
	}
	testutil.FailErr(t, "commit page 1", f.store.CommitPage(t.Context(), f.coord, wB.WorkerID, 0, page1.ID))

	page2, err := f.store.OpenPageBounded(t.Context(), f.coord, wB.WorkerID, 0, 10, 12000)
	testutil.FailErr(t, "open bounded page 2", err)
	if len(page2.Messages) != 1 || page2.More {
		t.Fatalf("page2 len=%d more=%v", len(page2.Messages), page2.More)
	}
	testutil.FailErr(t, "commit page 2", f.store.CommitPage(t.Context(), f.coord, wB.WorkerID, 0, page2.ID))

	pendingAfter, err := f.store.PendingCoordinatorMessageWorkers(t.Context(), f.coord)
	testutil.FailErr(t, "pending after beta drained", err)
	if len(pendingAfter) != 1 || pendingAfter[0].ID != wA.WorkerID {
		t.Fatalf("pending after = %+v, want [%s]", pendingAfter, wA.WorkerID)
	}

	unreadA, err := f.store.UnreadCoordinatorCount(t.Context(), f.coord, wA.WorkerID)
	testutil.FailErr(t, "unread alpha", err)
	if unreadA != 1 {
		t.Fatalf("unread alpha = %d, want 1", unreadA)
	}
	unreadB, err := f.store.UnreadCoordinatorCount(t.Context(), f.coord, wB.WorkerID)
	testutil.FailErr(t, "unread beta", err)
	if unreadB != 0 {
		t.Fatalf("unread beta = %d, want 0", unreadB)
	}
	if _, err := f.store.UnreadCoordinatorCount(t.Context(), workerrecords.Actor{}, wA.WorkerID); err == nil {
		t.Fatal("expected actor check error on UnreadCoordinatorCount")
	}
	if _, err := f.store.UnreadCoordinatorCount(t.Context(), f.coord, "wkr-missing"); err == nil {
		t.Fatal("expected unknown worker error on UnreadCoordinatorCount")
	}

	deliveredB, err := f.store.DeliveredThrough(t.Context(), f.coord, wB.WorkerID, f.coord.SessionID)
	testutil.FailErr(t, "delivered through beta", err)
	if deliveredB != 2 {
		t.Fatalf("delivered through beta = %d, want 2", deliveredB)
	}
	emptyRecipient, err := f.store.DeliveredThrough(t.Context(), f.coord, wB.WorkerID, " ")
	testutil.FailErr(t, "delivered through empty recipient", err)
	if emptyRecipient != 0 {
		t.Fatalf("delivered through empty recipient = %d, want 0", emptyRecipient)
	}
	if _, err := f.store.DeliveredThrough(t.Context(), workerrecords.Actor{}, wB.WorkerID, f.coord.SessionID); err == nil {
		t.Fatal("expected actor check error on DeliveredThrough")
	}
	if _, err := f.store.DeliveredThrough(t.Context(), f.coord, "wkr-missing", f.coord.SessionID); err == nil {
		t.Fatal("expected unknown worker error on DeliveredThrough")
	}
}
