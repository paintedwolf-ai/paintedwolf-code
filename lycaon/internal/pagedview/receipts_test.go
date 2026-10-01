package pagedview

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandJournalRetainsEarlyRetriesPastFormerLimit(t *testing.T) {
	commands := NewCommands[int](nil)
	t.Cleanup(commands.Close)
	initial := commands.Revision()
	calls := 0
	commit := func(string) (int, error) { calls++; return calls, nil }
	for i := range 4097 {
		_, _, err := commands.Apply(t.Context(), fmt.Sprint(i), commands.Revision(), []byte("disclose"), commit)
		testutil.FailErr(t, "apply retained command", err)
	}
	result, _, err := commands.Apply(t.Context(), "0", initial, []byte("disclose"), commit)
	testutil.FailErr(t, "retry earliest command", err)
	if result != 1 || calls != 4097 {
		t.Fatalf("retry result=%d, commits=%d", result, calls)
	}
}

func TestReceiptReservationPreventsUncertainReplay(t *testing.T) {
	r := &Receipts{}
	t.Cleanup(r.Close)
	testutil.FailErr(t, "reserve uncertain action", r.Reserve(t.Context(), "view", "request", []byte("digest")))
	_, found, err := r.Lookup(t.Context(), "view", "request")
	if !found || !errors.Is(err, ErrExpired) {
		t.Fatalf("uncertain result: found=%v err=%v", found, err)
	}
	testutil.FailErr(t, "record accepted action", r.Complete(t.Context(), "view", "request", []byte("accepted")))
	saved, found, err := r.Lookup(t.Context(), "view", "request")
	testutil.FailErr(t, "read accepted action", err)
	if !found || string(saved.Value) != "accepted" {
		t.Fatalf("saved=%+v found=%v", saved, found)
	}
	testutil.FailErr(t, "release receipt namespace", r.Release("view"))
	_, found, err = r.Lookup(t.Context(), "view", "request")
	testutil.FailErr(t, "read released namespace", err)
	if found {
		t.Fatal("released namespace retained records")
	}
}

func TestCommandJournalRejectsOversizedReceiptWithoutReplayingMutation(t *testing.T) {
	commands := NewCommands[[]byte](nil)
	t.Cleanup(commands.Close)
	initial := commands.Revision()
	calls := 0
	commit := func(string) ([]byte, error) { calls++; return make([]byte, receiptPayloadBytes), nil }
	for range 2 {
		_, _, err := commands.Apply(t.Context(), "request", initial, []byte("intent"), commit)
		if !errors.Is(err, ErrExpired) {
			t.Fatalf("receipt failure=%v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("uncertain mutation ran %d times", calls)
	}
	_, _, err := commands.Apply(t.Context(), "next", commands.Revision(), []byte("next intent"), func(string) ([]byte, error) {
		calls++
		return []byte("accepted"), nil
	})
	testutil.FailErr(t, "continue after uncertain receipt", err)
	if calls != 2 {
		t.Fatalf("next mutation ran %d times", calls-1)
	}
}

func TestAcceptedReceiptSurvivesRequestCancellation(t *testing.T) {
	commands := NewCommands[int](nil)
	t.Cleanup(commands.Close)
	ctx, cancel := context.WithCancel(t.Context())
	initial := commands.Revision()
	calls := 0
	commit := func(string) (int, error) { calls++; cancel(); return calls, nil }
	result, _, err := commands.Apply(ctx, "accepted", initial, []byte("expand"), commit)
	testutil.FailErr(t, "record canceled response", err)
	retried, _, err := commands.Apply(t.Context(), "accepted", initial, []byte("expand"), commit)
	testutil.FailErr(t, "retry accepted action", err)
	if result != 1 || retried != 1 || calls != 1 {
		t.Fatalf("result=%d retry=%d commits=%d", result, retried, calls)
	}
	_, _, err = commands.Apply(ctx, "canceled", commands.Revision(), []byte("expand"), commit)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled command: err=%v commits=%d", err, calls)
	}
}

func TestReceiptWriteFailuresAreIsolatedToTheirRequest(t *testing.T) {
	for _, operation := range []string{"complete", "abort", "release", "oversize"} {
		t.Run(operation, func(t *testing.T) {
			r := &Receipts{}
			t.Cleanup(r.Close)
			testutil.FailErr(t, "reserve failing action", r.Reserve(t.Context(), "failed", "request", []byte("digest")))
			_, err := r.db.ExecContext(t.Context(), `CREATE TRIGGER fail_update BEFORE UPDATE ON receipts WHEN OLD.namespace='failed' BEGIN SELECT RAISE(FAIL,'injected write failure'); END;
CREATE TRIGGER fail_delete BEFORE DELETE ON receipts WHEN OLD.namespace='failed' BEGIN SELECT RAISE(FAIL,'injected cleanup failure'); END;`)
			testutil.FailErr(t, "inject journal failures", err)
			switch operation {
			case "complete":
				err = r.Complete(t.Context(), "failed", "request", []byte("accepted"))
			case "abort":
				err = r.Abort(t.Context(), "failed", "request")
			case "release":
				err = r.Release("failed")
			case "oversize":
				err = r.Complete(t.Context(), "failed", "request", make([]byte, receiptPayloadBytes+1))
			}
			if err == nil || (operation != "release" && !errors.Is(err, ErrExpired)) {
				t.Fatalf("uncertain receipt: %v", err)
			}
			_, found, err := r.Lookup(t.Context(), "failed", "request")
			if !found || !errors.Is(err, ErrExpired) {
				t.Fatalf("uncertain request became replayable: found=%v err=%v", found, err)
			}
			for _, namespace := range []string{"failed", "healthy"} {
				testutil.FailErr(t, "reserve independent action", r.Reserve(t.Context(), namespace, "next", []byte("next")))
			}
			testutil.FailErr(t, "complete healthy action", r.Complete(t.Context(), "healthy", "next", []byte("accepted")))
			saved, found, err := r.Lookup(t.Context(), "healthy", "next")
			testutil.FailErr(t, "read healthy action", err)
			if !found || string(saved.Value) != "accepted" {
				t.Fatalf("healthy receipt=%+v found=%v", saved, found)
			}
		})
	}
}
