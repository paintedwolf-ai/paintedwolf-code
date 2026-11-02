package command

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestConcurrentProgramsKeepTheirOwnBareWaitJobs(t *testing.T) {
	registry := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	supervisor := &ProgramSupervisor{Background: registry}
	jobs := supervisor.Jobs("session", "project")
	start := func(id string) (context.Context, string) {
		admitted, err := jobs.BeginProgram(t.Context())
		testutil.FailErr(t, "begin overlapping program", err)
		root := toolcontract.OperationInvocation{SessionID: "session", Identity: toolcontract.ExecutionIdentity{ProgramID: id, ExecutionID: id}}
		var handle string
		_, err = supervisor.RunProgram(admitted, root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			var startErr error
			handle, startErr = supervisor.StartJob(ctx, root, func(ctx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
				noteJobNative(ctx, []string{"waiting"})
				<-ctx.Done()
				return toolcontract.ProducerResult{Status: 130}, ctx.Err()
			})
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
		})
		testutil.FailErr(t, "start overlapping job", err)
		supervisor.AfterJobs(id, func(bool) {})
		handle, err = jobs.Resolve(handle)
		testutil.FailErr(t, "resolve job", err)
		return admitted, handle
	}
	firstCtx, first := start("first")
	secondCtx, second := start("second")
	for _, check := range []struct {
		ctx    context.Context
		handle string
	}{{firstCtx, first}, {secondCtx, second}} {
		got, err := jobs.Unwaited(check.ctx)
		testutil.FailErr(t, "read own wait set", err)
		if !slices.Equal(got, []string{check.handle}) {
			t.Fatalf("wait set %v, want only %s", got, check.handle)
		}
	}
	nextCtx, err := jobs.BeginProgram(t.Context())
	testutil.FailErr(t, "begin inspection program", err)
	got, err := jobs.Unwaited(nextCtx)
	testutil.FailErr(t, "read session wait set", err)
	if !slices.Equal(got, []string{first, second}) {
		t.Fatalf("session wait set %v", got)
	}
}
