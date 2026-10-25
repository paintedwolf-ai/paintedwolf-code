package bgprocess

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestProgramCompletionRetainsNoticeAdmissionUntilConsumption(t *testing.T) {
	repo := t.TempDir()
	gittest.Init(t, repo)
	testutil.FailErr(t, "tracked file", os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("saved\n"), 0o644))
	gittest.CommitAll(t, repo, "Initial")
	for _, mode := range []string{"consumer", "discard after transfer", "no consumer", "close", "dispose session"} {
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
			snapshot := indexwatch.Take(repo)
			defer snapshot.Release()
			if !snapshot.Active() {
				t.Fatal("index capture missing")
			}
			registered := make(chan struct{})
			registry := NewRegistry(DefaultConfig(), hooks)
			t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
			handle, err := registry.StartProgram(t.Context(), ProgramJobSpec{SessionID: "session", Run: func(ctx context.Context, _, _ io.Writer) (ProgramJobResult, error) {
				<-registered
				if mode == "close" || mode == "dispose session" {
					<-ctx.Done()
				}
				return ProgramJobResult{UnsavedDocumentPaths: []string{"source.txt"}, ReleaseNotices: release}, nil
			}})
			testutil.FailErr(t, "start", err)
			registry.Lifecycle.WatchIndex("session", handle, snapshot)
			close(registered)
			switch mode {
			case "consumer", "discard after transfer":
				var completion Completion
				select {
				case completion = <-completed:
				case <-time.After(5 * time.Second):
					t.Fatal("completion missing")
				}
				if memory.InUse().State != 128 || completion.ReleaseNotices == nil || len(completion.UnsavedDocumentPaths) != 1 {
					t.Fatalf("unadmitted completion: %+v memory=%+v", completion, memory.InUse())
				}
				if mode == "consumer" {
					testutil.FailErr(t, "close after transfer", registry.Lifecycle.Close(t.Context()))
				} else {
					registry.Lifecycle.Discard("session", handle)
				}
				if !completion.IndexWatch.Active() || !snapshot.Active() || memory.InUse().State != 128 {
					t.Fatal("process cleanup released transferred completion resources")
				}
				completion.IndexWatch.Release()
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
			if memory.InUse().State != 0 || releases.Load() != 1 || snapshot.Active() {
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
