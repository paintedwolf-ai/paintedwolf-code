package command

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestProgramSessionDisposalDrainsAndRefusesRetainedAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	supervisor := &ProgramSupervisor{Background: registry}
	jobs := supervisor.Jobs("owned", "project")
	sibling := supervisor.Jobs("sibling", "project")
	admitted, err := jobs.BeginProgram(ctx)
	testutil.FailErr(t, "admit actual session owner", err)
	root := toolcontract.OperationInvocation{SessionID: "owned", Identity: toolcontract.ExecutionIdentity{ProgramID: "program", ExecutionID: "root"}}
	var retained context.Context
	var handle string
	_, err = supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
		retained = runCtx
		var startErr error
		handle, startErr = supervisor.StartJob(runCtx, root, func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			noteJobNative(jobCtx, []string{"controlled native fixture"})
			<-jobCtx.Done()
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
		})
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
	})
	testutil.FailErr(t, "register actual held job", err)
	handle, err = jobs.Resolve(handle)
	testutil.FailErr(t, "resolve held job public identity", err)
	// A new foreground keeps active jobs, while completed prior-program payloads
	// and signals follow the same pruning boundary as their handle order.
	jobs.mu.Lock()
	jobs.results = map[string]jobOutcome{"old": {}, handle: {}}
	jobs.signals = map[string]int{"old": 143, handle: 130}
	jobs.order = append([]string{"old"}, jobs.order...)
	jobs.mu.Unlock()
	_, err = jobs.BeginProgram(ctx)
	testutil.FailErr(t, "prune prior program outcomes", err)
	if len(jobs.results) != 1 || len(jobs.signals) != 1 || len(jobs.order) != 1 || jobs.order[0] != handle {
		t.Fatalf("pruning discarded active job or retained old payloads: %+v", jobs)
	}
	settled := make(chan bool, 1)
	supervisor.AfterJobs("program", func(stopped bool) { settled <- stopped })
	testutil.FailErr(t, "dispose actual job owner", supervisor.DisposeSession(ctx, "owned"))
	if !testutil.Receive(t, "stopped program settlement", settled) || registry.HasRunning("owned") {
		t.Fatal("disposal did not drain and settle the held job")
	}
	if _, err := supervisor.StartJob(retained, root, func(context.Context, string, commandprogram.Streams) (toolcontract.ProducerResult, error) {
		t.Error("retained child reached its owner after disposal")
		return toolcontract.ProducerResult{}, nil
	}); !errors.Is(err, ErrProgramStopped) {
		t.Fatalf("late child admission = %v", err)
	}
	if _, err := jobs.BeginProgram(ctx); !errors.Is(err, ErrProgramStopped) {
		t.Fatalf("retained session owner reopened: %v", err)
	}
	root.Identity.ProgramID = "late-program"
	if result, err := supervisor.RunProgram(admitted, root, func(context.Context) (toolcontract.ProducerResult, error) {
		t.Error("pre-disposal admission reopened after index removal")
		return toolcontract.ProducerResult{}, nil
	}); !errors.Is(err, ErrProgramStopped) || result.Terminal != toolcontract.ProducerInterrupted || result.Status != 130 || result.StopCode == "" {
		t.Fatalf("stale admission token = %v, result=%+v", err, result)
	}
	if len(supervisor.sessions) != 1 || supervisor.sessions["sibling"] != sibling || len(supervisor.programs) != 0 {
		t.Fatal("disposal retained owned state or disturbed sibling state")
	}
	if _, err := sibling.BeginProgram(ctx); err != nil {
		t.Fatalf("sibling admission was closed: %v", err)
	}
}

func TestProgramRetainsFinishedRawHandleUntilRegistryEviction(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	registry := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 1, MaxRecent: 1}, bgprocess.Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	supervisor := &ProgramSupervisor{Background: registry}
	jobs := supervisor.Jobs("session", "project")
	start := func(id string, status int) string {
		t.Helper()
		admitted, err := jobs.BeginProgram(ctx)
		testutil.FailErr(t, "begin next program", err)
		root := toolcontract.OperationInvocation{SessionID: "session", Identity: toolcontract.ExecutionIdentity{ProgramID: id, ExecutionID: id}}
		var handle string
		_, err = supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
			var startErr error
			handle, startErr = supervisor.StartJob(runCtx, root, func(context.Context, string, commandprogram.Streams) (toolcontract.ProducerResult, error) {
				return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted}, nil
			})
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
		})
		testutil.FailErr(t, "start real completed job", err)
		handle, err = jobs.Resolve(handle)
		testutil.FailErr(t, "resolve completed job public identity", err)
		supervisor.AfterJobs(id, func(bool) {})
		if _, _, err := registry.Lifecycle.Wait(ctx, "session", handle); err != nil {
			t.Fatalf("await real job: %v", err)
		}
		return handle
	}
	first := start("first", 23)
	_, err := jobs.BeginProgram(ctx)
	testutil.FailErr(t, "begin foreground after job exit", err)
	result, err := jobs.Wait(ctx, first, nil, "wait")
	testutil.FailErr(t, "wait completed raw handle across programs", err)
	if result.Status != 23 || len(jobs.order) != 0 {
		t.Fatalf("raw-handle result=%+v live order=%v", result, jobs.order)
	}
	second := start("second", 42)
	_, err = jobs.BeginProgram(ctx)
	testutil.FailErr(t, "prune actual evicted history", err)
	if _, err := jobs.Resolve(first); err == nil {
		t.Fatal("evicted registry handle retained outcome authority")
	}
	result, err = jobs.Wait(ctx, second, nil, "wait")
	testutil.FailErr(t, "wait newest retained raw handle", err)
	if result.Status != 42 || len(jobs.results) != 1 {
		t.Fatalf("bounded retained results=%v latest=%+v", jobs.results, result)
	}
}

func awaitProgramDrainSignal(t *testing.T, step string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: lifecycle signal did not arrive", step)
	}
}

func TestProgramSessionDisposalRetryWaitsForHeldJob(t *testing.T) {
	registry := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 1}, bgprocess.Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	supervisor := &ProgramSupervisor{Background: registry}
	jobs := supervisor.Jobs("owned", "project")
	sibling := supervisor.Jobs("sibling", "project")
	admitted, err := jobs.BeginProgram(t.Context())
	testutil.FailErr(t, "admit original session owner", err)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	root := toolcontract.OperationInvocation{SessionID: "owned", Identity: toolcontract.ExecutionIdentity{ProgramID: "retry-program", ExecutionID: "root"}}
	_, err = supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
		_, startErr := supervisor.StartJob(runCtx, root, func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			noteJobNative(jobCtx, []string{"controlled native fixture"})
			close(started)
			<-jobCtx.Done()
			close(canceled)
			<-release
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
		})
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
	})
	testutil.FailErr(t, "start actual held program job", err)
	awaitProgramDrainSignal(t, "actual job running", started)
	settled := make(chan bool, 1)
	supervisor.AfterJobs(root.Identity.ProgramID, func(stopped bool) { settled <- stopped })
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := supervisor.DisposeSession(short, "owned"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initial program disposal = %v", err)
	}
	awaitProgramDrainSignal(t, "held job cancellation", canceled)
	if _, err := jobs.BeginProgram(t.Context()); !errors.Is(err, ErrProgramStopped) {
		t.Fatalf("timed-out disposal reopened retained owner: %v", err)
	}
	if _, err := sibling.BeginProgram(t.Context()); err != nil {
		t.Fatalf("owned disposal closed sibling admission: %v", err)
	}
	long, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	retry := make(chan error, 1)
	go func() { retry <- supervisor.DisposeSession(long, "owned") }()
	select {
	case err := <-retry:
		t.Fatalf("program disposal retry ignored its live held job: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-settled:
		t.Fatal("program settled before its held job exited")
	default:
	}
	finish()
	testutil.FailErr(t, "retry retained owner disposal", testutil.Receive(t, "actual retry completion", retry))
	if !testutil.Receive(t, "stopped program settlement", settled) || registry.HasRunning("owned") {
		t.Fatal("retry did not join and settle the actual stopped job")
	}
	if supervisor.sessions["owned"] != nil || supervisor.sessions["sibling"] != sibling {
		t.Fatal("retry retained the disposed owner or disturbed sibling state")
	}
}
