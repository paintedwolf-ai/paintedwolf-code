package invocation_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestAllToolCallsEndedReflectsInterruptRunning(t *testing.T) {
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

	_, err = rec.Begin(ctx, invocation.Start{
		ProjectID:  testdbseed.DefaultProjectID,
		SessionID:  "sess-1",
		ToolCallID: "call-1",
		ToolName:   "command",
		Args:       map[string]any{"command": "echo hi"},
		Contract:   contract,
	})
	testutil.FailErr(t, "begin call-1", err)

	// Running receipt must report ended=false.
	ended, err = rec.AllToolCallsEnded(ctx, "sess-1", []string{"call-1"})
	testutil.FailErr(t, "check running call", err)
	if ended {
		t.Fatal("expected false while invocation receipt is still running")
	}

	// After InterruptRunning (host restart), receipt is durably ended.
	n, err := rec.InterruptRunning(ctx)
	testutil.FailErr(t, "interrupt running", err)
	if n != 1 {
		t.Fatalf("expected 1 interrupted receipt, got %d", n)
	}
	ended, err = rec.AllToolCallsEnded(ctx, "sess-1", []string{"call-1"})
	testutil.FailErr(t, "check interrupted call", err)
	if !ended {
		t.Fatal("expected true after InterruptRunning transitioned receipt to interrupted")
	}
}
