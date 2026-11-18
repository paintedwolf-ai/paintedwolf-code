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
	svc := &Service{transcript: &signals, kicks: kicks}
	finish := anchor.InformRender(anchor.ProcessFinished)
	refusal := anchor.InformRender(anchor.ProcessRefused)

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
		kicks.QueueDeferred(session, finish, kick.WithSubject(handle),
			kick.WithCommandCompletion("handle="+handle), kick.WithDeliveryObservation(snapshot.ExitObservation))
		if index == 0 {
			registry.Observe(session, handle)
		}
	}
	kicks.QueueDeferred(session, refusal, kick.WithSubject(jobs[0]), kick.WithCommandRefusal("unshown_refusals=1"))
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
