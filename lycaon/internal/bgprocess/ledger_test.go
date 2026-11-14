package bgprocess

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// startGatedProgram starts a program job whose body runs once its handle is known.
func startGatedProgram(t *testing.T, r *Registry, session string, body func(context.Context, string) (ProgramJobResult, error)) string {
	t.Helper()
	gate := make(chan string, 1)
	handle, err := r.StartProgram(t.Context(), ProgramJobSpec{
		SessionID: session, CommandLine: "python3 -m http.server 8000 &", OriginTool: "command", ToolCallID: "call",
		Run: func(ctx context.Context, _, _ io.Writer) (ProgramJobResult, error) { return body(ctx, <-gate) },
	})
	testutil.FailErr(t, "start program job", err)
	gate <- handle
	return handle
}

func exitWith(code int) func(context.Context, string) (ProgramJobResult, error) {
	return func(context.Context, string) (ProgramJobResult, error) { return ProgramJobResult{ExitCode: code}, nil }
}

func awaitJob(t *testing.T, r *Registry, session, handle string) {
	t.Helper()
	_, _, err := r.Lifecycle.Wait(t.Context(), session, handle)
	testutil.FailErr(t, "wait for program job", err)
}

func ledgerHandles(r *Registry, session string) []string {
	var handles []string
	for _, job := range r.LedgerJobs(session) {
		handles = append(handles, job.Handle)
	}
	return handles
}

func TestProgramJobCarriesItsStagesSettledRefusals(t *testing.T) {
	var mu sync.Mutex
	var completions []Completion
	r := NewRegistry(DefaultConfig(), Hooks{Complete: func(_ context.Context, completion Completion) {
		mu.Lock()
		completions = append(completions, completion)
		mu.Unlock()
	}})
	refusals := confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{
		{Operation: "network-bind", Target: "*:8000", Count: 1, Recovery: confine.RecoverLocalListen},
	}}
	handle := startGatedProgram(t, r, "session", func(ctx context.Context, handle string) (ProgramJobResult, error) {
		settle := r.AttachProgramStage(ctx, "session", handle, ProgramStage{Boundary: confine.Boundary{Applied: true}, Lease: &confine.ActionLease{}})
		settle(refusals, nil)
		settle(confine.SandboxRefusals{Refusals: []confine.SandboxRefusal{{Operation: "signal", Count: 1}}}, nil)
		return ProgramJobResult{ExitCode: 1}, nil
	})
	awaitJob(t, r, "session", handle)

	snapshot, err := r.Output.ReadRawOutput("session", handle, 0)
	testutil.FailErr(t, "read program output", err)
	if got := snapshot.Confinement.Refusals(); !reflect.DeepEqual(got, refusals) || !snapshot.Confinement.Boundary.Applied {
		t.Fatalf("output confinement = %+v refusals %+v, want %+v", snapshot.Confinement.Boundary, got, refusals)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(completions) != 1 || !reflect.DeepEqual(completions[0].Observation.Refusals, refusals) {
		t.Fatalf("completion refusals = %+v", completions)
	}
	jobs := r.LedgerJobs("session")
	if len(jobs) != 1 || !jobs[0].Exited || jobs[0].ExitCode != 1 || jobs[0].Refusals != len(refusals.Refusals) {
		t.Fatalf("ledger = %+v", jobs)
	}
}

func TestLedgerKeepsAnUnobservedExitUntilTheAgentObservesIt(t *testing.T) {
	r := NewRegistry(DefaultConfig(), Hooks{})
	handle := startGatedProgram(t, r, "session", exitWith(1))
	awaitJob(t, r, "session", handle)
	for range 2 {
		if got := ledgerHandles(r, "session"); !reflect.DeepEqual(got, []string{handle}) {
			t.Fatalf("ledger = %v, want the unobserved exit", got)
		}
	}
	if _, err := r.Output.ReadOutput(t.Context(), "session", handle); err != nil {
		t.Fatalf("person read output: %v", err)
	}
	if got := ledgerHandles(r, "session"); !reflect.DeepEqual(got, []string{handle}) {
		t.Fatalf("a person's read removed the exit from the agent's ledger: %v", got)
	}
	r.Observe("session", handle)
	if got := ledgerHandles(r, "session"); len(got) != 0 {
		t.Fatalf("observed exit stayed in the ledger: %v", got)
	}
}

func TestLedgerNeverListsAStoppedJobAsExited(t *testing.T) {
	r := NewRegistry(DefaultConfig(), Hooks{})
	handle := startGatedProgram(t, r, "session", func(ctx context.Context, _ string) (ProgramJobResult, error) {
		<-ctx.Done()
		return ProgramJobResult{ExitCode: 130}, ctx.Err()
	})
	if got := ledgerHandles(r, "session"); !reflect.DeepEqual(got, []string{handle}) {
		t.Fatalf("running job missing from ledger: %v", got)
	}
	_, err := r.Lifecycle.Stop("session", handle, TerminationStoppedByAgent)
	testutil.FailErr(t, "stop program job", err)
	awaitJob(t, r, "session", handle)
	if got := ledgerHandles(r, "session"); len(got) != 0 {
		t.Fatalf("stopped job listed: %v", got)
	}
}

func TestLedgerExitsFollowCompletedJobRetention(t *testing.T) {
	config := DefaultConfig()
	config.MaxRecent = 2
	r := NewRegistry(config, Hooks{})
	for range config.MaxRecent + 2 {
		handle := startGatedProgram(t, r, "session", exitWith(1))
		awaitJob(t, r, "session", handle)
	}
	if got := r.LedgerJobs("session"); len(got) != config.MaxRecent {
		t.Fatalf("ledger holds %d exits, retention keeps %d", len(got), config.MaxRecent)
	}
}
