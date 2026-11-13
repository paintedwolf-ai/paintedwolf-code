package bgprocess

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A job blocked on a person reports so while it runs, and its completion
// separates that wait from run time.
func TestProgramJobReportsItsWaitOnAPerson(t *testing.T) {
	completed := make(chan Completion, 1)
	registry := NewRegistry(DefaultConfig(), Hooks{Complete: func(_ context.Context, completion Completion) { completed <- completion }})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	finish := make(chan struct{})
	handle, err := registry.StartProgram(t.Context(), ProgramJobSpec{SessionID: "session", Run: func(context.Context, io.Writer, io.Writer) (ProgramJobResult, error) {
		<-finish
		return ProgramJobResult{}, nil
	}})
	testutil.FailErr(t, "start", err)
	awaiting := func() bool {
		snapshot, err := registry.Output.ReadRawOutput("session", handle, 0)
		testutil.FailErr(t, "read output", err)
		return snapshot.Output.AwaitingPerson
	}
	if awaiting() {
		t.Fatal("awaiting before any wait")
	}
	endFirst := registry.BeginPersonWait("session", handle)
	endSecond := registry.BeginPersonWait("session", handle)
	endFirst()
	endFirst()
	if !awaiting() {
		t.Fatal("overlapping wait closed by the other's end")
	}
	endSecond()
	if awaiting() {
		t.Fatal("still awaiting after every wait ended")
	}
	registry.BeginPersonWait("session", "unknown")()
	close(finish)
	var completion Completion
	select {
	case completion = <-completed:
	case <-time.After(testutil.Timeout(time.Minute)):
		t.Fatal("completion missing")
	}
	if completion.PersonWait < 0 || completion.PersonWait > completion.Elapsed() || completion.RunTime() != completion.Elapsed()-completion.PersonWait {
		t.Fatalf("person wait %v not part of elapsed %v (run %v)", completion.PersonWait, completion.Elapsed(), completion.RunTime())
	}
}
