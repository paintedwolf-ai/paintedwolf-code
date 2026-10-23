package bgprocess

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestProgramCompletionRetainsNoticeAdmissionUntilConsumption(t *testing.T) {
	for _, mode := range []string{"consumer", "no consumer", "close", "dispose session"} {
		t.Run(mode, func(t *testing.T) {
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1024, State: 1024})
			testutil.FailErr(t, "memory", err)
			lease, err := memory.ReserveProgramState(t.Context(), 128)
			testutil.FailErr(t, "notice projection", err)
			var releases atomic.Int32
			released := make(chan struct{})
			release := sync.OnceFunc(func() { releases.Add(1); lease(); close(released) })
			defer release()
			completed := make(chan Completion, 1)
			hooks := Hooks{}
			if mode != "no consumer" {
				hooks.Complete = func(_ context.Context, completion Completion) { completed <- completion }
			}
			registry := NewRegistry(DefaultConfig(), hooks)
			t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
			_, err = registry.StartProgram(t.Context(), ProgramJobSpec{SessionID: "session", Run: func(ctx context.Context, _, _ io.Writer) (ProgramJobResult, error) {
				if mode == "close" || mode == "dispose session" {
					<-ctx.Done()
				}
				return ProgramJobResult{UnsavedDocumentPaths: []string{"source.txt"}, ReleaseNotices: release}, nil
			}})
			testutil.FailErr(t, "start", err)
			switch mode {
			case "consumer":
				var completion Completion
				select {
				case completion = <-completed:
				case <-time.After(5 * time.Second):
					t.Fatal("completion missing")
				}
				if memory.InUse().State != 128 || completion.ReleaseNotices == nil || len(completion.UnsavedDocumentPaths) != 1 {
					t.Fatalf("unadmitted completion: %+v memory=%+v", completion, memory.InUse())
				}
				completion.ReleaseNotices()
				completion.ReleaseNotices()
			case "close":
				testutil.FailErr(t, "close", registry.Lifecycle.Close(t.Context()))
			case "dispose session":
				testutil.FailErr(t, "dispose", registry.Lifecycle.DisposeSession(t.Context(), "session"))
			}
			select {
			case <-released:
			case <-time.After(5 * time.Second):
				t.Fatal("notice admission not released")
			}
			if memory.InUse().State != 0 || releases.Load() != 1 {
				t.Fatalf("notice lifetime: memory=%+v releases=%d", memory.InUse(), releases.Load())
			}
			if mode == "close" || mode == "dispose session" {
				select {
				case completion := <-completed:
					t.Fatalf("discarded completion delivered: %+v", completion)
				default:
				}
			}
		})
	}
}
