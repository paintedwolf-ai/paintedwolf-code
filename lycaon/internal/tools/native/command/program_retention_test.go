package command

import (
	"context"
	"errors"
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
		handle, startErr = supervisor.StartJob(runCtx, root, func(jobCtx context.Context, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			<-jobCtx.Done()
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
		})
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
	})
	testutil.FailErr(t, "register actual held job", err)
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
	if _, err := supervisor.StartJob(retained, root, func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error) {
		t.Error("retained child reached its owner after disposal")
		return toolcontract.ProducerResult{}, nil
	}); !errors.Is(err, ErrProgramStopped) {
		t.Fatalf("late child admission = %v", err)
	}
	if _, err := jobs.BeginProgram(ctx); !errors.Is(err, ErrProgramStopped) {
		t.Fatalf("retained session owner reopened: %v", err)
	}
	root.Identity.ProgramID = "late-program"
	if _, err := supervisor.RunProgram(admitted, root, func(context.Context) (toolcontract.ProducerResult, error) {
		t.Error("pre-disposal admission reopened after index removal")
		return toolcontract.ProducerResult{}, nil
	}); !errors.Is(err, ErrProgramStopped) {
		t.Fatalf("stale admission token = %v", err)
	}
	if len(supervisor.sessions) != 1 || supervisor.sessions["sibling"] != sibling || len(supervisor.programs) != 0 {
		t.Fatal("disposal retained owned state or disturbed sibling state")
	}
	if _, err := sibling.BeginProgram(ctx); err != nil {
		t.Fatalf("sibling admission was closed: %v", err)
	}
}
