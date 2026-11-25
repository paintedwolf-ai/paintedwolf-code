package command

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
)

// parkedSettlement lets exactly one of the program's end or its promotion
// settle a parked call's lease.
type parkedSettlement struct {
	parked tools.ParkedProgram
	once   sync.Once
}

func (s *parkedSettlement) claim() bool {
	won := false
	s.once.Do(func() { won = true })
	return won
}

func (r *ProgramRuntime) settleParked(ctx context.Context, run *programRun, ending *programEnding, parked tools.ParkedProgram) {
	r.recordParkedRun(ctx, run)
	r.recordDetachedEffects(ctx, run)
	content, err := ending.content()
	parked.Settle(ctx, content, err)
}

// promoteParked continues a parked program whose stage the sandbox refused as
// a background job, and settles the lease with the job's handle. The program
// keeps running; the job reports the refusal and, later, the program's end.
func (r *ProgramRuntime) promoteParked(ctx context.Context, e *programEvaluation, ending *programEnding, settlement *parkedSettlement) {
	select {
	case <-ending.done:
		return
	default:
	}
	if !settlement.claim() {
		return
	}
	run := e.run
	handle, err := r.startProgramJob(ctx, e, ending, e.call.started)
	if err != nil {
		slog.WarnContext(ctx, "refused parked program stays parked", "program_id", run.root.Identity.ProgramID, "error", err)
		go func() {
			<-ending.done
			if errors.Is(err, errProgramOutputDetached) {
				// The job that failed to take the output records the program.
				content, contentErr := ending.content()
				settlement.parked.Settle(ctx, content, contentErr)
				return
			}
			r.settleParked(ctx, run, ending, settlement.parked)
		}()
		return
	}
	content, err := runningProgramResult(run, handle)
	settlement.parked.Settle(ctx, content, err)
}
