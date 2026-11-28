package command

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// RunningProgramResult identifies the original program; no statements are replayed.
type RunningProgramResult struct {
	ProgramID string `json:"program_id"`
	State     string `json:"state"`
	Handle    string `json:"handle"`
}

func (r *ProgramRuntime) continueProgram(ctx context.Context, e *programEvaluation, ending *programEnding, startedAt time.Time) (string, error) {
	run := e.run
	handle, err := r.startProgramJob(ctx, e, ending, startedAt)
	if err != nil {
		if !errors.Is(err, errProgramOutputDetached) {
			r.interruptProgram(ctx, run, ending)
			go func() {
				<-ending.done
				ending.releaseNotices()
			}()
		}
		return "", err
	}
	e.call.detach()
	if out := e.callTC.Effects.Out; out != nil {
		out.MergeChild(run.tc.Effects.Out.TakeCommitted(), tools.MergeProgram)
		if projection, err := r.Recorder.ProjectProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, 1); err == nil {
			out.CommandProgram = &projection.Program
		}
	}
	e.observeRunningConfinement()
	return runningProgramResult(run, handle)
}

// errProgramOutputDetached reports a job that started but could not take over
// the program's output; the job has already interrupted the program.
var errProgramOutputDetached = errors.New("program output could not follow its job")

// startProgramJob continues a running program as a background job that
// carries its stages and output, and returns the job's handle.
func (r *ProgramRuntime) startProgramJob(ctx context.Context, e *programEvaluation, ending *programEnding, startedAt time.Time) (string, error) {
	run := e.run
	attached := make(chan error, 1)
	registry, session := r.Supervisor.Background, run.tc.Identity.SessionID
	handle, err := e.stages.bind(func(history bgprocess.ProgramHistory) (string, error) {
		return registry.StartProgram(ctx, bgprocess.ProgramJobSpec{
			SessionID: session, RootSessionID: run.tc.ChatSessionID(),
			ProjectID: run.tc.Identity.ProjectID, Cwd: e.paths.Cwd().DisplayPath,
			CommandLine: run.source, OriginTool: run.tool, ToolCallID: run.tc.Identity.ToolCallID, StartedAt: startedAt,
			History: history,
			Run: func(jobCtx context.Context, stdout, stderr io.Writer) (bgprocess.ProgramJobResult, error) {
				stop := context.AfterFunc(jobCtx, func() { r.Supervisor.StopProgram(run.root.Identity.ProgramID) })
				defer stop()
				attachErr := errors.Join(e.stdout.follow(stdout), e.stderr.follow(stderr))
				attached <- attachErr
				if attachErr != nil {
					r.Supervisor.Interrupt(run.root.Identity.ProgramID)
				}
				<-ending.done
				r.recordParkedRun(context.WithoutCancel(ctx), run)
				r.recordDetachedEffects(context.WithoutCancel(ctx), run)
				return ending.jobResult(), errors.Join(ending.err, attachErr)
			},
		})
	}, func(handle string) func(bgprocess.ProgramStage) stageSettler {
		return func(stage bgprocess.ProgramStage) stageSettler {
			return registry.AttachProgramStage(context.WithoutCancel(ctx), session, handle, stage)
		}
	})
	if err != nil {
		return "", err
	}
	if err := <-attached; err != nil {
		return "", errors.Join(errProgramOutputDetached, err)
	}
	e.waits.promoted(func() func() { return registry.BeginPersonWait(session, handle) })
	return handle, nil
}

func runningProgramResult(run *programRun, handle string) (string, error) {
	result, err := surveyjson.Marshal(RunningProgramResult{ProgramID: run.root.Identity.ProgramID, State: "running", Handle: handle})
	return string(result), err
}

func (e *programEnding) jobResult() bgprocess.ProgramJobResult {
	outcome := e.settlement.outcome
	status := outcome.Status
	if e.err != nil && status == 0 {
		status = 1
	}
	settled := bgprocess.ProgramJobResult{
		ExitCode: status, FinishedAt: e.finishedAt, PersonWait: &e.personWait,
	}
	if e.settlement.releaseFeedback != nil {
		settled.ReleaseNotices = e.releaseNotices
	}
	settled.ProgramStop.Code = outcome.StopCode
	if refusal := outcome.Refusal; refusal != nil {
		settled.ProgramStop.Code, settled.ProgramStop.Feedback = refusal.Code, refusal.Feedback
	}
	return settled
}

// follow replays retained bytes and subscribes under the write lock, so output
// written during promotion is neither lost nor duplicated.
func (s *programStream) follow(writer io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spool != nil {
		reader, err := s.spool.Reader()
		if err != nil {
			return err
		}
		if _, err := io.Copy(writer, io.LimitReader(reader, s.size)); err != nil {
			return err
		}
	}
	s.follower = writer
	return nil
}
