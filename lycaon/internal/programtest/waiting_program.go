package programtest

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// WaitingProgram stands in for the evaluator: it prints "start", reports one
// wait state on its clock, then prints "done" when the test ends the wait or
// stops when the host interrupts it. A refused wait stops it at that wait.
type WaitingProgram struct {
	Clock *ManualClock
	State func(now time.Time) commandprogram.WaitState
	// Blocked closes once the program reported its wait.
	Blocked chan struct{}
	// IgnoresCancel keeps the program waiting through cancellation, as an
	// owner that does not unwind would.
	IgnoresCancel bool
	release       chan struct{}
}

func NewWaitingProgram(clock *ManualClock, state func(time.Time) commandprogram.WaitState) *WaitingProgram {
	return &WaitingProgram{Clock: clock, State: state, Blocked: make(chan struct{}), release: make(chan struct{})}
}

// SleepState is the wait state of `sleep N` started at now.
func SleepState(d time.Duration) func(time.Time) commandprogram.WaitState {
	return func(now time.Time) commandprogram.WaitState {
		return commandprogram.WaitState{Statement: fmt.Sprintf("sleep %g", d.Seconds()), Until: now.Add(d)}
	}
}

// EndWait lets the program's wait end, as its timer or jobs would.
func (p *WaitingProgram) EndWait() { close(p.release) }

func (p *WaitingProgram) Evaluate(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
	_, _ = fmt.Fprint(ports.Streams.Output, "start\n")
	unblock, err := ports.Waits.Block(ctx, p.State(p.Clock.Now()))
	close(p.Blocked)
	if err != nil {
		refused := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_LIMIT_EXCEEDED", EffectCertainty: toolcontract.NoEffect}
		return commandprogram.ProgramOutcome{Status: 1, Stop: &commandprogram.HostStop{Kind: commandprogram.StopOwner, Result: refused}}, err
	}
	defer unblock()
	canceled := ctx.Done()
	if p.IgnoresCancel {
		canceled = nil
	}
	select {
	case <-p.release:
		_, _ = fmt.Fprint(ports.Streams.Output, "done\n")
		return commandprogram.ProgramOutcome{}, nil
	case <-canceled:
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}
}
