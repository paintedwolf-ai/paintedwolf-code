package invocation_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestAllToolCallsEndedReflectsInterruptRunningAndJoinedControls(t *testing.T) {
	ctx := t.Context()
	db := testdbfixture.Open(t, "terminal-check.db")
	testdbseed.InsertSession(t, db, "sess-1", testdbseed.DefaultProjectID)
	rec := invocation.NewSQLRecorder(db)

	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}

	// Unknown call ID (no receipt) is not reported as ended.
	ended, err := rec.AllToolCallsEnded(ctx, "sess-1", []string{"call-unknown"})
	testutil.FailErr(t, "check unknown call", err)
	if ended {
		t.Fatal("expected false when no receipt exists")
	}

	beginCall := func(id string) {
		t.Helper()
		_, err := rec.Begin(ctx, invocation.Start{
			ProjectID:  testdbseed.DefaultProjectID,
			SessionID:  "sess-1",
			ToolCallID: id,
			ToolName:   "command",
			Args:       map[string]any{"command": "echo " + id},
			Contract:   contract,
		})
		testutil.FailErr(t, "begin "+id, err)
	}

	beginCall("call-1")
	beginCall("call-2")

	// Running receipts must report ended=false.
	ended, err = rec.AllToolCallsEnded(ctx, "sess-1", []string{"call-1"})
	testutil.FailErr(t, "check running call-1", err)
	if ended {
		t.Fatal("expected false while invocation receipt is still running")
	}

	// Interrupt both running calls (simulating host restart).
	n, err := rec.InterruptRunning(ctx)
	testutil.FailErr(t, "interrupt running", err)
	if n != 2 {
		t.Fatalf("expected 2 interrupted receipts, got %d", n)
	}

	// Joined all-terminal: both distinct IDs have durable terminal evidence -> true.
	ended, err = rec.AllToolCallsEnded(ctx, "sess-1", []string{"call-1", "call-2", "call-1"})
	testutil.FailErr(t, "check joined all-terminal", err)
	if !ended {
		t.Fatal("expected true when all distinct joined tool call IDs are terminal")
	}

	// Joined partial-missing: one terminal ID + one ID with no durable receipt -> false.
	ended, err = rec.AllToolCallsEnded(ctx, "sess-1", []string{"call-1", "call-missing"})
	testutil.FailErr(t, "check joined terminal+missing", err)
	if ended {
		t.Fatal("expected false when a joined tool call ID has no durable receipt")
	}

	// Joined one-live-owner: one terminal ID + one running ID -> false.
	beginCall("call-live")
	ended, err = rec.AllToolCallsEnded(ctx, "sess-1", []string{"call-1", "call-live"})
	testutil.FailErr(t, "check joined terminal+live", err)
	if ended {
		t.Fatal("expected false when one joined tool call ID is still running")
	}
}
