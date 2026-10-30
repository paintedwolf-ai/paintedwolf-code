package commandprogram

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

var errStageIncomplete = errors.New("pipeline producer did not complete its stream")

// pipelineStages flattens a | tree into its ordered stage statements. A stage
// carrying its own modifiers or redirects is a stage, not a further pipe.
func pipelineStages(c commandsurface.BinaryCommand) []commandsurface.Statement {
	var out []commandsurface.Statement
	var walk func(commandsurface.Statement)
	walk = func(s commandsurface.Statement) {
		if b, ok := s.Command.(commandsurface.BinaryCommand); ok && b.Operator == "|" && !s.Negated && !s.Background && len(s.Redirects) == 0 {
			walk(b.Left)
			walk(b.Right)
			return
		}
		out = append(out, s)
	}
	walk(c.Left)
	walk(c.Right)
	return out
}

type stageResult struct {
	executionID string
	result      toolcontract.ProducerResult
	cause       error
	err         error
	external    bool
}

// pipeline runs every stage concurrently in an isolated frame joined by metered
// byte pipes. Each stage closes its own endpoints when it settles, and every
// pipe is closed before return, so cancellation or an early consumer can never
// leave a producer blocked. SettlePipeline keeps every producer's outcome.
func (f *frame) pipeline(ctx context.Context, s commandsurface.Statement, c commandsurface.BinaryCommand) error {
	stages := pipelineStages(c)
	if int64(len(stages)) > f.e.limits["pipeline_stages"] {
		return f.refuseLimit(s.SyntaxLocation, "pipeline_stages", int64(len(stages)))
	}
	run, err := f.beginRecorded(ctx, s.SyntaxLocation, "|")
	if err != nil {
		return err
	}
	defer run.close(ctx)
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	activities := make([]*ExecutionWait, len(stages))
	for i := range activities {
		activities[i], _ = f.wait.Fork()
	}
	defer func() {
		for _, a := range activities {
			a.finish()
		}
	}()
	pipes := make([]*BytePipe, len(stages)-1)
	defer func() {
		for _, pipe := range pipes {
			if pipe != nil {
				_ = pipe.CloseConsumer()
			}
		}
	}()
	for i := range pipes {
		budget, err := NewByteBudget("stream_throughput_bytes", f.e.limits["stream_throughput_bytes"])
		if err == nil {
			pipes[i], err = NewBytePipe(pctx, budget)
		}
		if err != nil {
			return err
		}
	}
	stderr := &syncWriter{w: f.io.streams.Error}
	frames := make([]*frame, len(stages))
	releases := make([]func(), 0, len(stages))
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for i := range stages {
		g, release, err := f.isolate(pctx, nil)
		if err != nil {
			return f.stateRefusal(s.SyntaxLocation, err)
		}
		releases = append(releases, release)
		g.wait = activities[i]
		g.io.wait = g.wait
		g.parent, g.loops, g.returnable = run.inv, 0, false
		g.io.streams = f.io.streams
		g.io.setWriter(2, stderr)
		if i > 0 {
			g.io.setReader(0, pipes[i-1].ConsumerEnd())
		}
		if i < len(pipes) {
			g.producerPipe = pipes[i]
			g.io.setWriter(1, pipes[i])
		}
		frames[i] = g
	}
	unblock, err := f.wait.Block(ctx, WaitState{})
	if err != nil {
		return err
	}
	defer unblock()
	results := make([]stageResult, len(stages))
	var wg sync.WaitGroup
	for i := range stages {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer frames[i].wait.finish()
			results[i] = frames[i].stage(pctx, stages[i])
			if i < len(pipes) {
				_ = pipes[i].CloseProducer(results[i].incomplete())
			}
			if i > 0 {
				_ = frames[i].io.streams.Input.Close()
			}
		}(i)
	}
	wg.Wait()
	return f.settlePipeline(ctx, run, stages, pipes, results)
}

func (f *frame) settlePipeline(ctx context.Context, run *childRun, stages []commandsurface.Statement, pipes []*BytePipe, results []stageResult) error {
	outcomes := make([]PipelineStageOutcome, len(stages))
	var stageErr error
	for i, r := range results {
		outcomes[i] = PipelineStageOutcome{ExecutionID: r.executionID, Result: r.result, RequiredInputComplete: true}
		if i > 0 {
			termination := pipes[i-1].Completion().Termination
			outcomes[i].RequiredInputComplete = termination == StreamEOF || termination == StreamConsumerClosed
		}
		// The native byte-endpoint exception never applies to an external
		// producer; only the executor's own SIGPIPE classification may.
		var executor ExecutorClosure
		if i < len(pipes) && errors.As(r.cause, &executor) {
			outcomes[i].Closure = ExternalConsumerClosure(executor, r.executionID, results[i+1].executionID)
		} else if i < len(pipes) && !r.external {
			outcomes[i].Closure = NativeConsumerClosure(pipes[i], r.cause, r.executionID, results[i+1].executionID)
		}
		if stageErr == nil && r.err != nil {
			stageErr = r.err
		}
	}
	out, settleErr := SettlePipeline(run.inv.Identity.ExecutionID, outcomes, f.state.latch)
	f.publish.mark(out.PublicationEligible)
	f.state.pipeStatus = out.PipeStatuses
	f.state.vars.SetPipeStatus(out.PipeStatuses)
	aggregate := toolcontract.ProducerResult{Status: out.Status, Terminal: toolcontract.ProducerCompleted, Complete: out.Complete, EffectCertainty: toolcontract.NoEffect}
	if stop, ok := f.state.latch.Stop(); ok {
		aggregate = stop.Result
	}
	run.settle(ctx, aggregate, errors.Join(stageErr, settleErr))
	if err := f.state.control.Record(run.inv.Identity.ExecutionID, aggregate, true, false); err != nil {
		return err
	}
	if stop, ok := f.state.latch.Stop(); ok {
		for _, r := range results {
			if r.executionID == stop.ExecutionID && r.err != nil {
				return r.err
			}
		}
		if stageErr != nil {
			return stageErr
		}
		return &AdmissionStoppedError{Stop: stop}
	}
	return settleErr
}

// stage runs one stage statement and reports its producer: the stage's last
// settled execution when it carries the stage's final status, otherwise an
// aggregation-only record of that status (exit, negation, bookkeeping).
func (g *frame) stage(ctx context.Context, s commandsurface.Statement) stageResult {
	_, err := g.statement(ctx, s)
	if errors.Is(err, ErrConsumerClosed) && g.producerPipe != nil && g.producerPipe.Completion().Termination == StreamConsumerClosed {
		err = nil
	}
	if trapErr := runExitTrap(ctx, g, err); trapErr != nil && err == nil {
		err = trapErr
	}
	out := stageResult{err: err}
	_, stopped := g.state.latch.Stop()
	if last := g.state.last; last != nil && (stopped || last.Result.Status == g.state.control.Status()) {
		out.executionID, out.result, out.cause, out.external = last.Identity.ExecutionID, last.Result, last.Diagnostic, last.Command == "external"
		return out
	}
	out.executionID = uuid.NewString()
	out.result = ordinaryResult(g.state.control.Status())
	if stop, ok := g.state.latch.Stop(); ok {
		out.result = stop.Result
	}
	return out
}

func (r stageResult) incomplete() error {
	t := r.result.Terminal
	if r.err != nil || r.result.StopCode != "" || (t != toolcontract.ProducerCompleted && t != toolcontract.ProducerFailed) {
		return errStageIncomplete
	}
	return nil
}
