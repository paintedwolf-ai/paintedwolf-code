package bgprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTeardownRetainsReasonAndCustodyWithoutPublishingRetiredJobs(t *testing.T) {
	for _, reason := range []TerminationReason{TerminationChatDeleted, TerminationProjectDeleted, TerminationRootDetached, TerminationWorkerCompleted, TerminationWorkerCancelled, TerminationAppQuit} {
		t.Run(string(reason), func(t *testing.T) {
			completed := make(chan Completion, 1)
			events := make(chan api.BackgroundProcessEvent, 8)
			registry := NewRegistry(Config{}, Hooks{
				Complete: func(_ context.Context, c Completion) { completed <- c },
				Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
			})
			owned := startControlledDrainJob(t, registry, "session", "project")
			t.Cleanup(func() { owned.release(); _ = registry.Lifecycle.Close(context.Background()) })
			proc, err := registry.jobs.lookup("session", owned.handle)
			testutil.FailErr(t, "capture physical job", err)
			dispose := func(ctx context.Context) error {
				if reason == TerminationAppQuit {
					return registry.Lifecycle.Close(ctx)
				}
				return registry.Lifecycle.DisposeSession(ctx, "session", reason)
			}
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			if err := dispose(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled physical wait = %v", err)
			}
			awaitDrainSignal(t, "teardown cancellation", owned.canceled)
			registry.jobs.mu.Lock()
			retainedReason, discarded := proc.reason, proc.discarded
			registry.jobs.mu.Unlock()
			if retainedReason != reason || !discarded || !registry.HasRunningHandles("session", []string{owned.handle}) {
				t.Fatalf("retired physical job lost its reason or custody: reason=%q discarded=%t", retainedReason, discarded)
			}
			owned.release()
			testutil.FailErr(t, "join retired physical job", dispose(t.Context()))
			if registry.HasRunning("session") {
				t.Fatal("teardown returned with physical jobs")
			}
			select {
			case c := <-completed:
				t.Fatalf("retired job published a new completion: %+v", c)
			default:
			}
			for {
				select {
				case ev := <-events:
					if ev.Stream == "exit" {
						t.Fatalf("retired job published an exit event: %+v", ev)
					}
				default:
					return
				}
			}
		})
	}
}

func TestFailedAdmissionPreservesCauseAndSettlesEveryTeardown(t *testing.T) {
	for _, reason := range []TerminationReason{"", TerminationStoppedByAgent, TerminationStoppedByUser, TerminationTimedOut, TerminationExited, TerminationChatDeleted, TerminationProjectDeleted, TerminationRootDetached, TerminationWorkerCompleted, TerminationWorkerCancelled, TerminationAppQuit} {
		t.Run(string(reason), func(t *testing.T) {
			for _, hidden := range []bool{false, true} {
				registry := NewRegistry(Config{}, Hooks{})
				proc := &Process{Handle: "admitted", SessionID: "session", running: true,
					processRuntimeState: processRuntimeState{reason: reason, done: make(chan struct{})}}
				registry.jobs.sessions[proc.SessionID] = map[string]*Process{proc.Handle: proc}
				if hidden {
					registry.jobs.mu.Lock()
					registry.jobs.beginDrainLocked(proc.SessionID)
					registry.jobs.mu.Unlock()
				}
				cause := errors.New("launch failed")
				err := registry.jobs.failStart(proc, cause)
				if !errors.Is(err, cause) {
					t.Fatalf("lost launch cause: %v", err)
				}
				wantClosed := reason == TerminationAppQuit
				wantTeardown := reason == TerminationChatDeleted || reason == TerminationProjectDeleted || reason == TerminationRootDetached || reason == TerminationWorkerCompleted || reason == TerminationWorkerCancelled
				if errors.Is(err, ErrRegistryClosed) != wantClosed || errors.Is(err, ErrTeardownInProgress) != wantTeardown {
					t.Fatalf("wrong admission cause for %q: %v", reason, err)
				}
				select {
				case <-proc.done:
				default:
					t.Fatal("failed launch retained physical completion")
				}
				if proc.running || !proc.hasExit || proc.exitCode != -1 || proc.reason != reason {
					t.Fatalf("failed launch state = %+v", proc)
				}
				testutil.FailErr(t, "join failed launch", registry.Lifecycle.Close(t.Context()))
			}
		})
	}
}
