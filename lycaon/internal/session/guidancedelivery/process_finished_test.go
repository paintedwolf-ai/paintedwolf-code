package guidancedelivery

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedSignals []string

func (r *recordedSignals) Append(_ context.Context, _ string, messages ...api.Message) error {
	for _, msg := range messages {
		*r = append(*r, msg.HostSignalID)
	}
	return nil
}

func TestDeliverPendingObservesOneExitAndKeepsSeparateRefusals(t *testing.T) {
	const session = "session"
	cfg := bgprocess.DefaultConfig()
	cfg.MaxRecent = 1
	registry := bgprocess.NewRegistry(cfg, bgprocess.Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	var signals recordedSignals
	svc := &Service{transcript: &signals}
	svc.Bind(kicks, anchor.NewBus(kicks))
	testutil.FailErr(t, "install anchor registry", svc.InstallAnchorRegistry())

	// A read shows the first terminal state after its signal queues.
	var jobs []string
	for index := range 2 {
		handle, err := registry.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
			SessionID: session,
			Run: func(context.Context, io.Writer, io.Writer) (bgprocess.ProgramJobResult, error) {
				return bgprocess.ProgramJobResult{ExitCode: 1}, nil
			},
		})
		testutil.FailErr(t, "start job", err)
		_, _, err = registry.Lifecycle.Wait(t.Context(), session, handle)
		testutil.FailErr(t, "finish job", err)
		jobs = append(jobs, handle)
		snapshot, err := registry.Output.ReadRawOutput(session, handle, 0)
		testutil.FailErr(t, "read terminal state", err)
		svc.Emit(t.Context(), session, anchor.ProcessFinished, anchor.Envelope{
			Subject: handle, CommandCompletionDigest: "handle=" + handle, CommandExitObservation: snapshot.ExitObservation,
		})
		if index == 0 {
			registry.Observe(session, handle)
		}
	}
	if known, _ := registry.State(session, jobs[0]); known {
		t.Fatal("completed job retention did not evict the older observed job")
	}
	svc.Emit(t.Context(), session, anchor.ProcessRefused, anchor.Envelope{
		Subject: jobs[0], CommandRefusalDigest: "unshown_refusals=1",
	})
	for range 2 {
		testutil.FailErr(t, "deliver queued guidance", svc.DeliverPending(t.Context(), session))
	}
	want := []string{anchor.ProcessFinished.String(), anchor.ProcessRefused.String()}
	if !slices.Equal(signals, want) {
		t.Fatalf("delivered signals = %v, want %v", signals, want)
	}
	if ledger := registry.LedgerJobs(session); len(ledger) != 0 {
		t.Fatalf("delivered exits stayed in the ledger: %+v", ledger)
	}
}
