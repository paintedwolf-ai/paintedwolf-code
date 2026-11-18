package submissions

import (
	"context"
	"errors"
	"io"
	"testing"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWaitAdmissionObservesOnlyDeliveredExits(t *testing.T) {
	const session = "session"
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	handle, err := registry.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID: session,
		Run: func(context.Context, io.Writer, io.Writer) (bgprocess.ProgramJobResult, error) {
			return bgprocess.ProgramJobResult{}, nil
		},
	})
	testutil.FailErr(t, "start job", err)
	_, _, err = registry.Lifecycle.Wait(t.Context(), session, handle)
	testutil.FailErr(t, "finish job", err)
	svc := &Service{}
	svc.SetProcessObserver(registry.Observe)
	winner := awaitstore.Condition{Kind: "process_done", Outcome: "satisfied", Handles: []string{handle}, Report: "exit_code=0"}
	errAck := errors.New("failed acknowledgement")
	deliver := svc.waitObservation(session, winner, func() error { return errAck })
	if err := deliver(); !errors.Is(err, errAck) {
		t.Fatalf("delivery error = %v, want %v", err, errAck)
	}
	refusal := winner
	refusal.Outcome = "refused"
	testutil.FailErr(t, "admit refusal", svc.waitObservation(session, refusal, nil)())
	if rows := registry.LedgerJobs(session); len(rows) != 1 || rows[0].Handle != handle {
		t.Fatalf("undelivered exit missing from ledger: %+v", rows)
	}
	testutil.FailErr(t, "admit completion", svc.waitObservation(session, winner, nil)())
	if rows := registry.LedgerJobs(session); len(rows) != 0 {
		t.Fatalf("delivered exit stayed in ledger: %+v", rows)
	}
}
