package bgprocess

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeliverableFinishedHandlesTracksSettledUnobservedJobs(t *testing.T) {
	var nilReg *Registry
	if got := DeliverableFinishedHandles(nilReg, "s1", nil); got != nil {
		t.Fatalf("nil registry handles = %v", got)
	}

	registry := NewRegistry(DefaultConfig(), Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

	hold := make(chan struct{})
	runningHandle, err := registry.StartProgram(t.Context(), ProgramJobSpec{
		SessionID: "s1",
		Run: func(ctx context.Context, _, _ io.Writer) (ProgramJobResult, error) {
			select {
			case <-ctx.Done():
				return ProgramJobResult{}, ctx.Err()
			case <-hold:
				return ProgramJobResult{}, nil
			}
		},
	})
	testutil.FailErr(t, "start running job", err)

	finishedHandle, err := registry.StartProgram(t.Context(), ProgramJobSpec{
		SessionID: "s1",
		Run: func(_ context.Context, out, _ io.Writer) (ProgramJobResult, error) {
			_, _ = io.WriteString(out, "ready\n")
			return ProgramJobResult{}, nil
		},
	})
	testutil.FailErr(t, "start finished job", err)
	_, _, err = registry.Lifecycle.Wait(t.Context(), "s1", finishedHandle)
	testutil.FailErr(t, "wait finished job", err)

	gotAll := DeliverableFinishedHandles(registry, "s1", nil)
	if !slices.Equal(gotAll, []string{finishedHandle}) {
		t.Fatalf("all deliverable finished = %v, want [%s]", gotAll, finishedHandle)
	}
	if gotFiltered := DeliverableFinishedHandles(registry, "s1", []string{runningHandle}); len(gotFiltered) != 0 {
		t.Fatalf("running handle included in finished = %v", gotFiltered)
	}
	if gotExact := DeliverableFinishedHandles(registry, "s1", []string{finishedHandle}); !slices.Equal(gotExact, []string{finishedHandle}) {
		t.Fatalf("exact finished = %v, want [%s]", gotExact, finishedHandle)
	}
	close(hold)
}
