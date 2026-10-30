package command_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		owners, err := original(tc, paths)
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
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("utility never started")
	}
	if releases.Load() != 0 {
		t.Fatal("utility released while background job owns it")
	}
	close(finish)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("settled utility never released")
	}
	// A subsequent program exercises pruning and settlement without replaying
	// the prior owner's release callback.
	_, err = f.call(t, t.Context(), "true")
	testutil.FailErr(t, "run next program", err)
	if releases.Load() != 2 {
		t.Fatalf("utility lifetimes released %d times; want once per program", releases.Load())
	}
}
