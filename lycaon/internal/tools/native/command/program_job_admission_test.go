package command

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func noJobBody(context.Context, string, commandprogram.Streams) (toolcontract.ProducerResult, error) {
	return toolcontract.ProducerResult{}, nil
}

// A job starts only inside a live program bound to its own session owner and
// initiating call; every other admission returns no job, so no ID or handle
// can name a job that never ran.
func TestJobAdmissionRefusalsStartNothing(t *testing.T) {
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	origin := ProgramJobOrigin{Tool: "command", ToolCallID: "call-1", RootSessionID: "root"}
	owner := func() *SessionJobs {
		return &SessionJobs{Registry: registry, Supervisor: &ProgramSupervisor{Background: registry}, SessionID: "s1"}
	}
	bound := func(j *SessionJobs, o ProgramJobOrigin) context.Context {
		return context.WithValue(t.Context(), sessionJobsKey{}, programJobsBinding{owner: j, origin: o})
	}
	closed := owner()
	closed.closed.Store(true)
	other := owner()
	for _, tc := range []struct {
		name    string
		start   func() (commandprogram.StartedJob, error)
		stopped bool
	}{
		{"supervisor without a registry", func() (commandprogram.StartedJob, error) {
			return (&ProgramSupervisor{}).StartJob(t.Context(), toolcontract.OperationInvocation{SessionID: "s1"}, "sleep 1", noJobBody)
		}, false},
		{"no supervised program", func() (commandprogram.StartedJob, error) {
			return (&ProgramSupervisor{Background: registry}).StartJob(t.Context(), toolcontract.OperationInvocation{SessionID: "s1"}, "sleep 1", noJobBody)
		}, true},
		{"owner without a registry", func() (commandprogram.StartedJob, error) {
			return (&SessionJobs{}).start(t.Context(), "sleep 1", noJobBody)
		}, false},
		{"no body", func() (commandprogram.StartedJob, error) {
			j := owner()
			return j.start(bound(j, origin), "sleep 1", nil)
		}, false},
		{"no initiating call", func() (commandprogram.StartedJob, error) {
			return owner().start(t.Context(), "sleep 1", noJobBody)
		}, false},
		{"another owner's binding", func() (commandprogram.StartedJob, error) {
			return owner().start(bound(other, origin), "sleep 1", noJobBody)
		}, false},
		{"incomplete origin", func() (commandprogram.StartedJob, error) {
			j := owner()
			return j.start(bound(j, ProgramJobOrigin{Tool: "command"}), "sleep 1", noJobBody)
		}, false},
		{"closed owner", func() (commandprogram.StartedJob, error) {
			return closed.start(bound(closed, origin), "sleep 1", noJobBody)
		}, true},
		{"outside supervision", func() (commandprogram.StartedJob, error) {
			j := owner()
			return j.start(bound(j, origin), "sleep 1", noJobBody)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, err := tc.start()
			if err == nil {
				t.Fatalf("admitted a job: %+v", started)
			}
			if started != (commandprogram.StartedJob{}) {
				t.Fatalf("refused admission named a job: %+v", started)
			}
			if errors.Is(err, ErrProgramStopped) != tc.stopped {
				t.Fatalf("err=%v, want stopped=%v", err, tc.stopped)
			}
		})
	}
	if registry.HasRunning("s1") {
		t.Fatal("refused admissions left a running job")
	}
}

// A job is named by its statement's source, and by a fixed label whenever
// the span names no text inside that source.
func TestJobCommandLineReadsOnlyItsSpan(t *testing.T) {
	source := "python3 -m http.server 8000 &  \n   &"
	for _, tc := range []struct {
		start, end int
		want       string
	}{
		{0, 27, "python3 -m http.server 8000"},
		{0, 31, "python3 -m http.server 8000 &"},
		{29, 35, "background job"},
		{-1, 5, "background job"},
		{10, 10, "background job"},
		{10, 5, "background job"},
		{0, len(source) + 1, "background job"},
	} {
		if got := jobCommandLine(source, toolcontract.SourceSpan{StartByte: tc.start, EndByte: tc.end}); got != tc.want {
			t.Errorf("span [%d,%d) = %q, want %q", tc.start, tc.end, got, tc.want)
		}
	}
}
