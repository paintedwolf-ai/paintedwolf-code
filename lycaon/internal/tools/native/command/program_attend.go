package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// interruptGrace bounds how long an interruption waits for the program to
// unwind before it reports the program from its receipts.
const interruptGrace = 10 * time.Second

// SourceRunRecorder records a parked program's run as verification evidence,
// as the tool loop records an inline call's.
type SourceRunRecorder interface {
	RecordSourceRunEvidence(ctx context.Context, sessionID string, sess *api.Session, tool string, run tools.SourceRunCapture)
}

// ParkedProgramResult is a command call's result when its program parked: the
// turn ends, and the program's complete result resumes it.
type ParkedProgramResult struct {
	ProgramID string `json:"program_id"`
	State     string `json:"state"`
	// Reason is the statement the program waits at.
	Reason  string `json:"reason"`
	LeaseID string `json:"lease_id"`
	WakeAt  string `json:"wake_at,omitempty"`
}

// programEvaluation is one bound program handed from its call to evaluation.
type programEvaluation struct {
	run          *programRun
	call         *programCall
	park         *programPark
	plan         commandsurface.ProgramSnapshot
	ports        commandprogram.EvaluationPorts
	dispatcher   *ProgramDispatcher
	stdout       *programStream
	stderr       *programStream
	outputs      *invocation.ProgramOutputRetention
	scope        captureprojection.Scope
	documents    *programDocuments
	paths        *projectpaths.ProgramPaths
	verification func() commandSourceIdentity
	release      func()
	// callTC is the call's own tool context; evaluation writes its effects to
	// run.tc's private outcome until the result belongs to the call.
	callTC tools.ToolContext
}

// programEnding is a program's result once evaluation and settlement end.
type programEnding struct {
	done   chan struct{}
	result string
	err    error
}

// attend holds the call until its program ends, or parks the call when the
// program waits past the inline budget.
func (r *ProgramRuntime) attend(callCtx context.Context, e *programEvaluation) (string, error) {
	ending := &programEnding{done: make(chan struct{})}
	go func() {
		ending.result, ending.err = r.finish(e)
		close(ending.done)
	}()
	for waiting := true; waiting; {
		select {
		case <-ending.done:
			waiting = false
		case blocked := <-e.park.parks:
			if parked, ok := r.parkProgram(callCtx, e, ending, blocked); ok {
				return parked, nil
			}
		}
	}
	e.call.detach()
	if out := e.callTC.Effects.Out; out != nil {
		out.CommandProgram, out.SourceRun = e.run.tc.Effects.Out.CommandProgram, e.run.tc.Effects.Out.SourceRun
		out.Facts = out.Facts.Merge(e.run.tc.Effects.Out.Facts)
		if e.run.tc.Effects.Out.ExternalAccess != nil {
			out.ExternalAccess = e.run.tc.Effects.Out.ExternalAccess
		}
	}
	return ending.result, ending.err
}

// parkProgram ends the call's turn on the session's wait lease. The program
// keeps running under supervision; its end settles the lease. A wait that
// ended before the park claimed it, or a session that cannot park, keeps the
// program in the foreground.
func (r *ProgramRuntime) parkProgram(callCtx context.Context, e *programEvaluation, ending *programEnding, blocked *blockedWait) (string, bool) {
	if r.Parking == nil || callCtx.Err() != nil {
		return "", false
	}
	select {
	case <-ending.done:
		// The wait ended before the park; the result belongs to the call.
		return "", false
	default:
	}
	run, state := e.run, blocked.state
	programID := run.root.Identity.ProgramID
	parked, err := r.Parking.ParkProgram(callCtx, e.callTC, tools.ProgramPark{
		ProgramID: programID, Reason: state.Statement, Deadline: e.call.deadline,
		Claim:     func() bool { return e.park.claim(blocked) },
		Interrupt: func(ctx context.Context) string { return r.interruptProgram(ctx, run, ending) },
	})
	if err != nil {
		e.park.release()
		if !errors.Is(err, tools.ErrParkUnavailable) && !errors.Is(err, tools.ErrWaitEnded) {
			slog.WarnContext(callCtx, "command program stays in the foreground", "program_id", programID, "error", err)
		}
		return "", false
	}
	e.call.detach()
	settleCtx := context.WithoutCancel(callCtx)
	go func() {
		<-ending.done
		r.recordParkedRun(settleCtx, run)
		parked.Settle(settleCtx, ending.result, ending.err)
	}()
	if out := e.callTC.Effects.Out; out != nil {
		if projection, err := r.Recorder.ProjectProgram(callCtx, run.tc.Identity.SessionID, programID, 1); err == nil {
			receipt := projection.Program
			out.CommandProgram = &receipt
		}
	}
	result := ParkedProgramResult{ProgramID: programID, State: "parked", Reason: state.Statement, LeaseID: parked.LeaseID()}
	if !state.Until.IsZero() {
		result.WakeAt = state.Until.UTC().Format(time.RFC3339)
	}
	content, err := surveyjson.Marshal(result)
	if err != nil {
		return "", false
	}
	return string(content), true
}

// interruptProgram stops a parked program at its waiting statement and
// returns its result. A program that does not unwind within the grace is
// reported from its receipts.
func (r *ProgramRuntime) interruptProgram(ctx context.Context, run *programRun, ending *programEnding) string {
	r.Supervisor.Interrupt(run.root.Identity.ProgramID)
	expired := make(chan struct{})
	stop := r.clock().AfterFunc(interruptGrace, func() { close(expired) })
	defer stop()
	select {
	case <-ending.done:
		if ending.err == nil {
			return ending.result
		}
	case <-ctx.Done():
	case <-expired:
	}
	result, err := r.ReceiptResult(context.WithoutCancel(ctx), run.tc.Identity.SessionID, run.root.Identity.ProgramID)
	if err != nil {
		return ""
	}
	return result
}

// recordParkedRun records the run evidence an inline call would have stamped.
func (r *ProgramRuntime) recordParkedRun(ctx context.Context, run *programRun) {
	capture := run.tc.Effects.Out.SourceRun
	if r.SourceRuns == nil || capture == nil {
		return
	}
	sess, err := r.Sessions.Get(ctx, run.tc.Identity.SessionID)
	if err != nil || sess == nil {
		return
	}
	r.SourceRuns.RecordSourceRunEvidence(ctx, sess.ID, sess, "command", *capture)
}

// ReceiptResult is a program's result as its durable receipts record it, for
// a program whose live result the host no longer holds, such as one a restart
// interrupted. Its children's outcomes stay in the receipt page.
func (r *ProgramRuntime) ReceiptResult(ctx context.Context, sessionID, programID string) (string, error) {
	if r == nil || r.Recorder == nil {
		return "", fmt.Errorf("command program runtime is not configured")
	}
	projection, err := r.Recorder.ProjectProgram(ctx, sessionID, programID, 1)
	if err != nil {
		return "", err
	}
	outcome := ProgramOutcome{
		ProgramID: programID, State: projection.Program.State, Status: 130, StopCode: projection.Program.StopCode,
		ExecutedChildren: projection.ExecutedChildren, CompletedEffects: projection.CompletedEffects,
		UncertainEffects: projection.UncertainEffects, NativeGaps: projection.NativeGaps,
	}
	if status := projection.Program.StatusCode; status != nil {
		outcome.Status = *status
	}
	if outcome.StopCode == "" && outcome.State != "completed" {
		outcome.StopCode = toolrejection.ToolOwnerInterruptedCode
	}
	content, err := surveyjson.Marshal(outcome)
	return string(content), err
}

// finish evaluates the program under supervision, settles it and returns its
// bounded result, whether the call still holds it or it parked.
func (r *ProgramRuntime) finish(e *programEvaluation) (string, error) {
	run, ctx := e.run, e.call.ctx
	defer e.call.end()
	var evaluated commandprogram.ProgramOutcome
	result, evalErr := r.Supervisor.RunProgram(ctx, run.root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
		var err error
		evaluated, err = r.Evaluate(ctx, e.plan, e.ports)
		return evaluatedResult(evaluated), err
	})
	e.park.settle()
	settled := make(chan error, 1)
	lifetime := &programLifetime{release: e.release}
	defer lifetime.callerDone()
	r.Supervisor.AfterJobs(run.root.Identity.ProgramID, func(stopped bool) {
		settled <- r.settleProgram(ctx, run, result, evaluated, stopped)
		lifetime.settled()
	})
	var settleErr error
	settledNow := false
	select {
	case settleErr = <-settled:
		settledNow = true
	default:
		// & jobs still run under supervision; the program settles when they end.
	}
	feedbackCtx := ctx
	if errors.Is(evalErr, ErrProgramInterrupted) {
		// The interruption canceled the program; its refusal still renders.
		feedbackCtx = context.WithoutCancel(ctx)
	}
	refusal, releaseFeedback, feedbackErr := r.feedback(feedbackCtx, run, e.dispatcher, e.stderr, evalErr)
	defer releaseFeedback()
	if feedbackErr != nil || settleErr != nil {
		return "", errors.Join(feedbackErr, settleErr)
	}
	// Settlement and bounded result projection survive cancellation; they cannot
	// start another program operation.
	outcome, err := r.outcome(context.WithoutCancel(ctx), run, e.outputs, result, e.stdout, e.stderr, e.scope)
	if err != nil {
		return "", err
	}
	outcome.Stderr = e.documents.appendNotes(outcome.Stderr)
	outcome.Refusal = refusal
	outcome.CPUTimeUnmeasured = evaluated.CPUTimeUnmeasured
	if settledNow {
		stampProgramRun(run.tc, run.source, e.paths.Cwd(), outcome, e.verification)
	}
	out, err := surveyjson.Marshal(outcome)
	if err != nil {
		return "", fmt.Errorf("command program encode: %w", err)
	}
	return string(out), nil
}

// programLifetime releases a program's plan and frame once both the tool call
// has finished with them and the program (with its & jobs) has settled.
type programLifetime struct {
	mu                     sync.Mutex
	callerFinished, isDone bool
	release                func()
}

func (l *programLifetime) callerDone() {
	l.mu.Lock()
	l.callerFinished = true
	done := l.isDone
	l.mu.Unlock()
	if done {
		l.release()
	}
}

func (l *programLifetime) settled() {
	l.mu.Lock()
	l.isDone = true
	finished := l.callerFinished
	l.mu.Unlock()
	if finished {
		l.release()
	}
}

// stampProgramRun states the settled program's run for verification evidence:
// passed on status 0, failed otherwise, unverifiable when the host stopped it.
// A program whose jobs still run states nothing.
func stampProgramRun(tc tools.ToolContext, source string, cwd projectpaths.Resolved, outcome ProgramOutcome, verification func() commandSourceIdentity) {
	if tc.Effects.Out == nil {
		return
	}
	verdict := api.SourceVerdictPassed
	switch {
	case outcome.Refusal != nil || outcome.State == "interrupted":
		verdict = api.SourceVerdictUnverifiable
	case outcome.Status != 0:
		verdict = api.SourceVerdictFailed
	}
	run := &tools.SourceRunCapture{CheckID: tc.Identity.ToolCallID, IsCheck: tc.Execution.VerificationCheck, Command: source, ExitCode: outcome.Status, Verdict: verdict, Cwd: cwd.DisplayPath}
	if tc.Execution.VerificationCheck && verification != nil {
		identity := verification()
		run.SourceRevision, run.SourceRootDigest = identity.Revision, identity.Root
	}
	tc.Effects.Out.SourceRun = run
}
