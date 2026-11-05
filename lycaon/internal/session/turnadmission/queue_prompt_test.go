package turnadmission

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/promptstate"
)

func TestRunLockedClassifiesCancellationBeforeReporting(t *testing.T) {
	cleanup := errors.New("cleanup failed")
	for _, tc := range []struct {
		name     string
		stop     string
		runErr   error
		wantStop bool
	}{
		{name: "active stop", stop: "active", runErr: context.Canceled, wantStop: true},
		{name: "finished stop", stop: "finished", runErr: context.Canceled, wantStop: true},
		{name: "wrapped cancellation", stop: "finished", runErr: fmt.Errorf("provider: %w", context.Canceled), wantStop: true},
		{name: "joined cancellations", stop: "active", runErr: errors.Join(context.Canceled, context.Canceled), wantStop: true},
		{name: "unexpected cancellation", runErr: context.Canceled},
		{name: "another session stopped", stop: "other", runErr: context.Canceled},
		{name: "joined cleanup failure", stop: "active", runErr: errors.Join(context.Canceled, cleanup)},
		{name: "wrapped joined cleanup", stop: "finished", runErr: fmt.Errorf("provider: %w", errors.Join(context.Canceled, cleanup))},
		{name: "deadline", stop: "active", runErr: context.DeadlineExceeded},
		{name: "provider failure", stop: "active", runErr: cleanup},
		{name: "completed result", stop: "finished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := lifecycle.New(nil)
			turns := &recordingTurns{}
			settlement := &recordingSettlement{}
			service := &Service{Gate: gate, Workspace: readyWorkspace{}, Turns: turns, Settlement: settlement}
			service.Runner = promptRunner(func() (*promptresult.Result, error) {
				if tc.stop != "" {
					id := "session"
					if tc.stop == "other" {
						id = "other"
					}
					flight, _ := gate.Begin(id)
					if tc.stop == "finished" {
						gate.Finish(id, flight, nil)
					}
				}
				return nil, tc.runErr
			})
			var locks promptstate.MutexRegistry
			lock := locks.Acquire("session")
			lock.Lock()
			_, got := service.RunLocked(t.Context(), "session", promptinput.Input{}, lock)
			if tc.wantStop {
				if !errors.Is(got, lifecycle.ErrStopping) || !errors.Is(turns.reported, lifecycle.ErrStopping) {
					t.Fatalf("stop classification: returned %v, reported %v", got, turns.reported)
				}
			} else if !errors.Is(got, tc.runErr) || !errors.Is(turns.reported, tc.runErr) {
				t.Fatalf("failure changed: returned %v, reported %v, want %v", got, turns.reported, tc.runErr)
			}
			wantDrain := tc.stop == "" || tc.stop == "other"
			if settlement.drained != wantDrain {
				t.Fatalf("drained = %v, want %v", settlement.drained, wantDrain)
			}
			probe := locks.Acquire("session")
			if !probe.TryLock() {
				t.Fatal("prompt lock was not released")
			}
			probe.Unlock()
		})
	}
}

type promptRunner func() (*promptresult.Result, error)

func (f promptRunner) Run(context.Context, string, promptinput.Input) (*promptresult.Result, error) {
	return f()
}

type readyWorkspace struct{}

func (readyWorkspace) CheckReady(context.Context, string) error { return nil }

type recordingTurns struct{ reported error }

func (r *recordingTurns) ReportFailure(_ context.Context, _ string, err error) error {
	r.reported = err
	return err
}
func (*recordingTurns) HostTurnBlocked(context.Context, string) bool { return false }

type recordingSettlement struct{ drained bool }

func (r *recordingSettlement) Drain(context.Context, string) error {
	r.drained = true
	return nil
}
