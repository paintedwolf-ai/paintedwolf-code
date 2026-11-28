package command_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type heldLifetimeBuiltin struct {
	finish  <-chan struct{}
	started chan<- struct{}
}

func (b heldLifetimeBuiltin) Invoke(ctx context.Context, _ commandprogram.OperationRequest, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
	close(b.started)
	select {
	case <-b.finish:
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
	case <-ctx.Done():
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, ctx.Err()
	}
}

func TestRecordedProgramRetainsUtilityUntilBackgroundSettlement(t *testing.T) {
	f := newProgramFixture(t)
	entered, finish, released := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releases atomic.Int32
	original := f.Runtime.Owners
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		owners, err := original(tc, paths, memory, admission)
		owners.Builtins = func(command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
			return map[string]commandprogram.OperationDispatcher{"cat": heldLifetimeBuiltin{finish: finish, started: entered}}, func() {
				if releases.Add(1) == 1 {
					close(released)
				}
			}, nil
		}
		return owners, err
	}
	_, err := f.call(t, t.Context(), "cat &")
	testutil.FailErr(t, "start actual background utility", err)
	testutil.AwaitClosed(t, "the utility starts", entered)
	if releases.Load() != 0 {
		t.Fatal("utility released while background job owns it")
	}
	close(finish)
	testutil.AwaitClosed(t, "the settled utility is released", released)
	// A subsequent program exercises pruning and settlement without replaying
	// the prior owner's release callback.
	_, err = f.call(t, t.Context(), "true")
	testutil.FailErr(t, "run next program", err)
	if releases.Load() != 2 {
		t.Fatalf("utility lifetimes released %d times; want once per program", releases.Load())
	}
}

func TestRecordedProgramSetupFailureReleasesPlanOnce(t *testing.T) {
	fixture := newProgramFixture(t)
	previous := fixture.Runtime.Owners
	setupErr := errors.New("utility setup failed")
	released := 0
	var memory *toolcontract.ProgramMemory
	fixture.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, admitted *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		memory = admitted
		owners, err := previous(tc, paths, admitted, spill)
		owners.Builtins = func(command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
			return nil, func() { released++ }, setupErr
		}
		return owners, err
	}
	_, err := fixture.call(t, t.Context(), "true")
	if !errors.Is(err, setupErr) || released != 1 || memory == nil {
		t.Fatalf("setup error=%v utility release count=%d memory=%v", err, released, memory)
	}
	if used := memory.InUse(); used.Working != 0 || used.State != 0 {
		t.Fatalf("setup retained plan or frame memory: %+v", used)
	}
}
