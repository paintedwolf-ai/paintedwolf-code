package queue

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func commitQueueMutation() error            { return nil }
func commitQueueTurn([]api.QueueItem) error { return nil }

func appendQueueItem(s *Store, sessionID, itemID, text string) api.QueueDraft {
	return s.AppendOrdered(sessionID, itemID, "person-1", text, 0, time.Time{})
}

func TestAppendAndSnapshot(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "first")
	d := appendQueueItem(s, "sess", "", "second")
	if len(d.QueueItems) != 2 {
		t.Fatalf("append count = %d, want 2", len(d.QueueItems))
	}
	if d.QueueItems[0].Text != "first" || d.QueueItems[1].Text != "second" {
		t.Fatalf("unexpected order: %+v", d.QueueItems)
	}
	if d.Revision != 2 {
		t.Fatalf("revision = %d, want 2", d.Revision)
	}
	if d.QueueItems[0].ID == "" || d.QueueItems[0].CreatedAt == "" {
		t.Fatalf("item missing id/created_at: %+v", d.QueueItems[0])
	}
}

func TestAppendOrderedUsesDurableAdmissionOrder(t *testing.T) {
	s := New()
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	s.AppendOrdered("sess", "later", "person-1", "second", 2, base.Add(-time.Hour))
	draft := s.AppendOrdered("sess", "earlier", "person-1", "first", 1, base)

	if len(draft.QueueItems) != 2 || draft.QueueItems[0].ID != "earlier" || draft.QueueItems[1].ID != "later" {
		t.Fatalf("durable order = %+v, want earlier then later", draft.QueueItems)
	}
}

func TestAppendOrderedDissolvesSplitGroup(t *testing.T) {
	s := New()
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	s.AppendOrdered("sess", "first", "person-1", "first", 1, base)
	draft := s.AppendOrdered("sess", "third", "person-1", "third", 3, base)
	if _, err := s.Link("sess", draft.Revision, []string{"first", "third"}); err != nil {
		testutil.FailErr(t, "link", err)
	}
	draft = s.AppendOrdered("sess", "second", "person-1", "second", 2, base)

	for _, item := range draft.QueueItems {
		if item.GroupID != "" {
			t.Fatalf("split group remained linked: %+v", draft.QueueItems)
		}
	}
}

func TestReorder(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	d := s.Snapshot("sess")
	got, err := s.Reorder("sess", d.Revision, []string{d.QueueItems[1].ID, d.QueueItems[0].ID})
	if err != nil {
		testutil.FailErr(t, "reorder", err)
	}
	if got.QueueItems[0].Text != "b" || got.QueueItems[1].Text != "a" {
		t.Fatalf("reorder did not swap: %+v", got.QueueItems)
	}
}

func TestReorderRejectsIncompleteList(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	d := s.Snapshot("sess")
	if _, err := s.Reorder("sess", d.Revision, []string{d.QueueItems[0].ID}); err == nil {
		t.Fatal("expected error for partial reorder list")
	}
}

func TestLinkUnlink(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	d := s.Snapshot("sess")
	linked, err := s.Link("sess", d.Revision, []string{d.QueueItems[0].ID, d.QueueItems[1].ID})
	if err != nil {
		testutil.FailErr(t, "link", err)
	}
	if linked.QueueItems[0].GroupID == "" || linked.QueueItems[0].GroupID != linked.QueueItems[1].GroupID {
		t.Fatalf("link did not share a group: %+v", linked.QueueItems)
	}
	unlinked, err := s.Unlink("sess", linked.Revision, []string{d.QueueItems[0].ID})
	if err != nil {
		testutil.FailErr(t, "unlink", err)
	}
	if unlinked.QueueItems[0].GroupID != "" {
		t.Fatalf("unlink did not clear group: %+v", unlinked.QueueItems[0])
	}
}

func TestLinkFailureIsAtomic(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	d := appendQueueItem(s, "sess", "", "b")
	before := s.Snapshot("sess")
	if _, err := s.Link("sess", d.Revision, []string{d.QueueItems[0].ID, "missing"}); err == nil {
		t.Fatal("expected link with an unknown item to fail")
	}
	after := s.Snapshot("sess")
	if after.Revision != before.Revision {
		t.Fatalf("failed link revision = %d, want %d", after.Revision, before.Revision)
	}
	for _, item := range after.QueueItems {
		if item.GroupID != "" {
			t.Fatalf("failed link partially grouped item: %+v", item)
		}
	}
}

func TestMutationRejectsStaleRevisionWithoutChangingDraft(t *testing.T) {
	s := New()
	stale := appendQueueItem(s, "sess", "", "a")
	current := appendQueueItem(s, "sess", "", "b")
	_, err := s.Remove("sess", stale.Revision, []string{stale.QueueItems[0].ID}, commitQueueMutation)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("remove error = %v, want ErrRevisionConflict", err)
	}
	after := s.Snapshot("sess")
	if after.Revision != current.Revision || len(after.QueueItems) != 2 {
		t.Fatalf("stale mutation changed draft: %+v", after)
	}
}

func TestDurableMutationFailureLeavesDraftUnpublished(t *testing.T) {
	s := New()
	draft := appendQueueItem(s, "sess", "", "a")
	wantErr := errors.New("receipt store unavailable")
	if _, err := s.Remove("sess", draft.Revision, []string{draft.QueueItems[0].ID}, func() error {
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Remove error = %v, want %v", err, wantErr)
	}
	after := s.Snapshot("sess")
	if after.Revision != draft.Revision || len(after.QueueItems) != 1 || after.QueueItems[0].ID != draft.QueueItems[0].ID {
		t.Fatalf("failed durable mutation published draft: %+v", after)
	}
}

func TestMoveFront(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	appendQueueItem(s, "sess", "", "c")
	d := s.Snapshot("sess")
	got, err := s.MoveFront("sess", d.Revision, []string{d.QueueItems[2].ID})
	if err != nil {
		testutil.FailErr(t, "move front", err)
	}
	if got.QueueItems[0].Text != "c" {
		t.Fatalf("move front: got %q, want c", got.QueueItems[0].Text)
	}
}

func TestTakeNextTurnUnlinkedIsSequential(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	first, ok, err := s.TakeNextTurn("sess", commitQueueTurn)
	testutil.FailErr(t, "take first turn", err)
	if !ok || len(first) != 1 || first[0].Text != "a" {
		t.Fatalf("first take: ok=%v items=%+v", ok, first)
	}
	second, ok, err := s.TakeNextTurn("sess", commitQueueTurn)
	testutil.FailErr(t, "take second turn", err)
	if !ok || len(second) != 1 || second[0].Text != "b" {
		t.Fatalf("second take: ok=%v items=%+v", ok, second)
	}
	if _, ok, err := s.TakeNextTurn("sess", commitQueueTurn); err != nil || ok {
		t.Fatal("expected empty after draining")
	}
}

func TestTakeNextTurnLinkedCoalesces(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	appendQueueItem(s, "sess", "", "c")
	d := s.Snapshot("sess")
	if _, err := s.Link("sess", d.Revision, []string{d.QueueItems[0].ID, d.QueueItems[1].ID}); err != nil {
		testutil.FailErr(t, "link", err)
	}
	turn, ok, err := s.TakeNextTurn("sess", commitQueueTurn)
	testutil.FailErr(t, "take linked turn", err)
	if !ok || len(turn) != 2 {
		t.Fatalf("linked take should coalesce: ok=%v items=%+v", ok, turn)
	}
	if turn[0].Text != "a" || turn[1].Text != "b" {
		t.Fatalf("linked order wrong: %+v", turn)
	}
	rest, ok, err := s.TakeNextTurn("sess", commitQueueTurn)
	testutil.FailErr(t, "take remaining turn", err)
	if !ok || len(rest) != 1 || rest[0].Text != "c" {
		t.Fatalf("remaining take: ok=%v items=%+v", ok, rest)
	}
}

func TestTakeNextTurnCommitFailureRetainsDraft(t *testing.T) {
	s := New()
	draft := appendQueueItem(s, "sess", "receipt-1", "a")
	wantErr := errors.New("receipt claim unavailable")
	if _, _, err := s.TakeNextTurn("sess", nil); err == nil {
		t.Fatal("take without receipt commit succeeded")
	}

	items, ok, err := s.TakeNextTurn("sess", func(pending []api.QueueItem) error {
		if len(pending) != 1 || pending[0].ID != "receipt-1" {
			t.Fatalf("pending turn = %+v", pending)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) || ok || items != nil {
		t.Fatalf("take = (%+v, %v, %v), want (nil, false, %v)", items, ok, err, wantErr)
	}
	after := s.Snapshot("sess")
	if after.Revision != draft.Revision || len(after.QueueItems) != 1 || after.QueueItems[0].ID != "receipt-1" {
		t.Fatalf("failed commit changed draft: before=%+v after=%+v", draft, after)
	}
}

func TestHoldSuppressesDrain(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	draft := s.Snapshot("sess")
	held, err := s.SetHold("sess", draft.Revision, true)
	if err != nil {
		testutil.FailErr(t, "hold", err)
	}
	if _, ok, err := s.TakeNextTurn("sess", commitQueueTurn); err != nil || ok {
		t.Fatal("hold should suppress drain")
	}
	if _, err := s.SetHold("sess", held.Revision, false); err != nil {
		testutil.FailErr(t, "release hold", err)
	}
	if _, ok, err := s.TakeNextTurn("sess", commitQueueTurn); err != nil || !ok {
		t.Fatal("releasing hold should allow drain")
	}
}

func TestRemoveAndClear(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	d := appendQueueItem(s, "sess", "", "b")
	if _, err := s.Remove("sess", d.Revision, []string{d.QueueItems[0].ID}, commitQueueMutation); err != nil {
		testutil.FailErr(t, "remove", err)
	}
	if got := s.Snapshot("sess"); len(got.QueueItems) != 1 || got.QueueItems[0].Text != "b" {
		t.Fatalf("remove left wrong state: %+v", got.QueueItems)
	}
	s.Clear("sess")
	if got := s.Snapshot("sess"); len(got.QueueItems) != 0 {
		t.Fatalf("clear did not empty draft: %+v", got.QueueItems)
	}
}

func TestRemoveMany(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	d := appendQueueItem(s, "sess", "", "c")
	removed, err := s.Remove("sess", d.Revision, []string{d.QueueItems[0].ID, d.QueueItems[2].ID}, commitQueueMutation)
	if err != nil {
		testutil.FailErr(t, "remove many", err)
	}
	if got := s.Snapshot("sess"); len(got.QueueItems) != 1 || got.QueueItems[0].Text != "b" {
		t.Fatalf("multi-remove left wrong state: %+v", got.QueueItems)
	}
	if _, err := s.Remove("sess", removed.Revision, []string{"nope"}, commitQueueMutation); err == nil {
		t.Fatal("remove of unknown id should fail and leave the draft unchanged")
	}
	if got := s.Snapshot("sess"); len(got.QueueItems) != 1 {
		t.Fatalf("failed remove mutated the draft: %+v", got.QueueItems)
	}
}

func TestGroupNormalization(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "a")
	appendQueueItem(s, "sess", "", "b")
	d := appendQueueItem(s, "sess", "", "c")
	ids := []string{d.QueueItems[0].ID, d.QueueItems[1].ID, d.QueueItems[2].ID}
	linked, err := s.Link("sess", d.Revision, ids[:2])
	if err != nil {
		testutil.FailErr(t, "link", err)
	}

	// Splitting a linked pair dissolves its group.
	got, err := s.Reorder("sess", linked.Revision, []string{ids[0], ids[2], ids[1]})
	if err != nil {
		testutil.FailErr(t, "reorder", err)
	}
	for _, it := range got.QueueItems {
		if it.GroupID != "" {
			t.Fatalf("split group should dissolve, still grouped: %+v", got.QueueItems)
		}
	}

	// Removing one member dissolves the singleton group.
	relinked, err := s.Link("sess", got.Revision, []string{ids[0], ids[2]})
	if err != nil {
		testutil.FailErr(t, "relink", err)
	}
	got, err = s.Remove("sess", relinked.Revision, []string{ids[2]}, commitQueueMutation)
	if err != nil {
		testutil.FailErr(t, "remove", err)
	}
	for _, it := range got.QueueItems {
		if it.GroupID != "" {
			t.Fatalf("single-member group should dissolve: %+v", got.QueueItems)
		}
	}
}

func TestUpdateText(t *testing.T) {
	s := New()
	d := appendQueueItem(s, "sess", "", "before")
	updated, err := s.UpdateText("sess", d.Revision, d.QueueItems[0].ID, "after", commitQueueMutation)
	if err != nil {
		testutil.FailErr(t, "update text", err)
	}
	if updated.QueueItems[0].Text != "after" {
		t.Fatalf("update did not replace text: %+v", updated.QueueItems)
	}
	if updated.Revision <= d.Revision {
		t.Fatalf("update did not bump revision: %d -> %d", d.Revision, updated.Revision)
	}
	if _, err := s.UpdateText("sess", updated.Revision, "nope", "x", commitQueueMutation); err == nil {
		t.Fatal("update of unknown id should fail")
	}
}

func TestCancelAllEmptiesDraftAndAdvancesRevision(t *testing.T) {
	s := New()
	before := appendQueueItem(s, "sess", "item", "queued")
	after := s.CancelAll("sess")
	if len(after.QueueItems) != 0 || after.Hold {
		t.Fatalf("canceled draft = %+v, want empty and unheld", after)
	}
	if after.Revision <= before.Revision {
		t.Fatalf("revision = %d, want greater than %d", after.Revision, before.Revision)
	}
	if snapshot := s.Snapshot("sess"); snapshot.Revision != after.Revision {
		t.Fatalf("snapshot revision = %d, want %d", snapshot.Revision, after.Revision)
	}
}

func TestRequestSendReservesExactHeadGroup(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "", "first")
	d := appendQueueItem(s, "sess", "", "second")
	ids := []string{d.QueueItems[0].ID, d.QueueItems[1].ID}
	_, err := s.Link("sess", d.Revision, ids)
	if err != nil {
		testutil.FailErr(t, "link", err)
	}
	d = appendQueueItem(s, "sess", "", "third")
	d, err = s.RequestSend("sess", d.Revision, func([]api.QueueItem) error { return nil })
	if err != nil {
		testutil.FailErr(t, "request send", err)
	}
	if !d.Sending || d.Hold {
		t.Fatalf("reserved draft = %+v", d)
	}
	var sent []api.QueueItem
	_, ok, err := s.TakeSendTurn("sess", func(items []api.QueueItem) error {
		sent = append(sent, items...)
		return nil
	})
	if err != nil {
		testutil.FailErr(t, "take send", err)
	}
	if !ok || len(sent) != 2 || sent[0].ID != ids[0] || sent[1].ID != ids[1] {
		t.Fatalf("sent = %+v want linked head %v", sent, ids)
	}
	remaining := s.Snapshot("sess")
	if remaining.Sending || len(remaining.QueueItems) != 1 || remaining.QueueItems[0].Text != "third" {
		t.Fatalf("remaining = %+v", remaining)
	}
}

func TestRequestSendFreezesDraftEditsButAllowsAppend(t *testing.T) {
	s := New()
	d := appendQueueItem(s, "sess", "", "first")
	d, err := s.RequestSend("sess", d.Revision, func([]api.QueueItem) error { return nil })
	if err != nil {
		testutil.FailErr(t, "request send", err)
	}
	if _, err := s.UpdateText("sess", d.Revision, d.QueueItems[0].ID, "changed", commitQueueMutation); err == nil {
		t.Fatal("update succeeded while send reserved")
	}
	after := appendQueueItem(s, "sess", "", "later")
	if !after.Sending || len(after.QueueItems) != 2 {
		t.Fatalf("append during send = %+v", after)
	}
}

func TestTakeSendTurnRetainsReservationWhenCommitFails(t *testing.T) {
	s := New()
	d := appendQueueItem(s, "sess", "", "first")
	if _, err := s.RequestSend("sess", d.Revision, func([]api.QueueItem) error { return nil }); err != nil {
		testutil.FailErr(t, "request send", err)
	}
	_, ok, err := s.TakeSendTurn("sess", func([]api.QueueItem) error { return errors.New("commit failed") })
	if err == nil || ok {
		t.Fatalf("take send = ok %v err %v", ok, err)
	}
	after := s.Snapshot("sess")
	if !after.Sending || len(after.QueueItems) != 1 {
		t.Fatalf("reservation lost after failure: %+v", after)
	}
}

func TestRequestSendCommitFailureLeavesDraftEditable(t *testing.T) {
	s := New()
	d := appendQueueItem(s, "sess", "", "first")
	before := d
	after, err := s.RequestSend("sess", d.Revision, func([]api.QueueItem) error {
		return errors.New("receipt update failed")
	})
	if err == nil {
		t.Fatal("request send succeeded after durable commit failed")
	}
	if after.Revision != before.Revision || after.Sending || len(after.QueueItems) != 1 {
		t.Fatalf("failed send changed draft: before=%+v after=%+v", before, after)
	}
	if _, err := s.UpdateText("sess", after.Revision, after.QueueItems[0].ID, "still editable", commitQueueMutation); err != nil {
		t.Fatalf("draft remained frozen after failed send: %v", err)
	}
}

func TestCancelSendReleasesTheReservationAndReopensEditing(t *testing.T) {
	s := New()
	s.AppendOrdered("s1", "a", "person-1", "first", 1, time.Time{})
	s.AppendOrdered("s1", "b", "person-1", "second", 2, time.Time{})
	draft, err := s.RequestSend("s1", s.Snapshot("s1").Revision, func([]api.QueueItem) error { return nil })
	if err != nil {
		t.Fatalf("RequestSend: %v", err)
	}
	if !draft.Sending {
		t.Fatal("RequestSend must reserve the head group")
	}
	if _, err := s.Remove("s1", draft.Revision, []string{"b"}, func() error { return nil }); !errors.Is(err, ErrSendReserved) {
		t.Fatalf("remove while sending = %v want ErrSendReserved", err)
	}

	var released []api.QueueItem
	draft, err = s.CancelSend("s1", draft.Revision, func(items []api.QueueItem) error {
		released = items
		return nil
	})
	if err != nil {
		t.Fatalf("CancelSend: %v", err)
	}
	// Only the reserved group is released. The receipts behind it are rewritten to drop
	// their continuation flag, and rewriting an unreserved neighbour's input would be a
	// write nobody asked for.
	if len(released) != 1 || released[0].ID != "a" {
		t.Fatalf("released = %+v want only the reserved head", released)
	}
	if draft.Sending {
		t.Fatal("CancelSend must clear the reservation")
	}
	if len(draft.QueueItems) != 2 {
		t.Fatalf("items = %d want both returned to the queue: %+v", len(draft.QueueItems), draft.QueueItems)
	}
	if _, err := s.Remove("s1", draft.Revision, []string{"b"}, func() error { return nil }); err != nil {
		t.Fatalf("editing must reopen after cancel: %v", err)
	}
}

func TestCancelSendWithoutAReservationIsRefused(t *testing.T) {
	s := New()
	s.AppendOrdered("s1", "a", "person-1", "first", 1, time.Time{})
	if _, err := s.CancelSend("s1", s.Snapshot("s1").Revision, func([]api.QueueItem) error { return nil }); !errors.Is(err, ErrNoSendToCancel) {
		t.Fatalf("cancel with no reservation = %v want ErrNoSendToCancel", err)
	}
}

func TestSendRejectionsCarryTypedSentinels(t *testing.T) {
	s := New()
	if _, err := s.RequestSend("missing", 0, func([]api.QueueItem) error { return nil }); !errors.Is(err, ErrNoDraft) {
		t.Fatalf("send with no draft = %v want ErrNoDraft", err)
	}
	s.AppendOrdered("s1", "a", "person-1", "first", 1, time.Time{})
	draft, err := s.RequestSend("s1", s.Snapshot("s1").Revision, func([]api.QueueItem) error { return nil })
	if err != nil {
		t.Fatalf("RequestSend: %v", err)
	}
	if _, err := s.RequestSend("s1", draft.Revision, func([]api.QueueItem) error { return nil }); !errors.Is(err, ErrSendPending) {
		t.Fatalf("second send = %v want ErrSendPending", err)
	}
}

func TestLinkRefusesItemsFromDifferentSenders(t *testing.T) {
	s := New()
	s.AppendOrdered("s1", "a", "person-1", "first", 1, time.Time{})
	draft := s.AppendOrdered("s1", "b", "person-2", "second", 2, time.Time{})
	if _, err := s.Link("s1", draft.Revision, []string{"a", "b"}); !errors.Is(err, ErrLinkAcrossSenders) {
		t.Fatalf("link across senders err=%v want ErrLinkAcrossSenders", err)
	}
	after := s.Snapshot("s1")
	for _, item := range after.QueueItems {
		if item.GroupID != "" {
			t.Fatalf("item %s grouped after refused link", item.ID)
		}
	}
	if _, err := s.Unlink("s1", after.Revision, []string{"a", "b"}); err != nil {
		t.Fatalf("unlink across senders: %v", err)
	}
}

func TestAwaitsPersonOnlyWhileHeldWithNothingReserved(t *testing.T) {
	s := New()
	appendQueueItem(s, "sess", "a", "first")
	d := appendQueueItem(s, "sess", "b", "second")
	if s.AwaitsPerson("sess", "a") {
		t.Fatal("an unheld draft item drains without a person")
	}
	d, err := s.SetHold("sess", d.Revision, true)
	testutil.FailErr(t, "hold draft", err)
	if !s.AwaitsPerson("sess", "a") || !s.AwaitsPerson("sess", "b") {
		t.Fatal("held draft items wait on the person editing them")
	}
	if _, err := s.RequestSend("sess", d.Revision, commitQueueTurn); err != nil {
		t.Fatalf("reserve head: %v", err)
	}
	if s.AwaitsPerson("sess", "a") || s.AwaitsPerson("sess", "b") {
		t.Fatal("Send releases the hold, so every item drains again")
	}
	if s.AwaitsPerson("sess", "missing") || s.AwaitsPerson("other", "a") {
		t.Fatal("an item outside the draft does not wait on the draft")
	}
}
