package command

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A launch stopped during the open started no process, so nothing states that one
// ran unobserved.
func TestLaunchStoppedDuringTheOpenReportsNoWindowFact(t *testing.T) {
	entered := make(chan struct{})
	window := &programWindow{due: true, clock: commandprogram.SystemClock(), ready: make(chan struct{}), open: func(ctx context.Context) commandWindow {
		close(entered)
		<-ctx.Done()
		return commandWindow{unobserved: true}
	}}
	launch, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := window.enter(launch)
		done <- err
	}()
	testutil.AwaitClosed(t, "the open began", entered)
	stop()
	if err := testutil.Receive(t, "the stopped launch", done); !errors.Is(err, context.Canceled) {
		t.Fatalf("enter = %v, want the stop", err)
	}
	window.settle(t.Context())
	if fact := window.fact(); fact != nil {
		t.Fatalf("fact = %+v, want none", fact)
	}
}
