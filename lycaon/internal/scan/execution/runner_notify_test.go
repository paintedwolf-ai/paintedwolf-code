package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type terminalContextKey struct{}

func TestNotifyTerminalDetachesFromScanLeaseCancellation(t *testing.T) {
	ctx := context.WithValue(t.Context(), terminalContextKey{}, "retained")
	ctx, cancel := context.WithCancel(ctx)
	cancel()

	called := false
	runner := &Runner{OnTerminal: func(callbackCtx context.Context, completed api.CodeScan) {
		called = true
		if err := callbackCtx.Err(); err != nil {
			t.Fatalf("terminal callback context: %v", err)
		}
		if got := callbackCtx.Value(terminalContextKey{}); got != "retained" {
			t.Fatalf("terminal callback context value = %v", got)
		}
		if completed.ID != "scan-1" {
			t.Fatalf("terminal callback scan id = %q", completed.ID)
		}
	}}

	runner.notifyTerminal(ctx, &api.CodeScan{ID: "scan-1"})
	if !called {
		t.Fatal("terminal callback was not called")
	}
}

func TestReconcileTerminalDetachesAndCanRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	want := errors.New("retry terminal delivery")
	calls := 0
	runner := &Runner{ReconcileTerminal: func(callbackCtx context.Context) error {
		calls++
		if err := callbackCtx.Err(); err != nil {
			t.Fatalf("reconcile context: %v", err)
		}
		if calls == 1 {
			return want
		}
		return nil
	}}

	if err := runner.reconcileTerminal(ctx); !errors.Is(err, want) {
		t.Fatalf("reconciliation error = %v, want %v", err, want)
	}
	testutil.FailErr(t, "retry reconciliation", runner.reconcileTerminal(ctx))
	if calls != 2 {
		t.Fatalf("terminal reconciliation calls = %d want 2", calls)
	}
}
