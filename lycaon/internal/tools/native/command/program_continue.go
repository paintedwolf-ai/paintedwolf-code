package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/runnerbudget"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// DefaultProgramInlineWait bounds how long an unparked program holds the agent.
const DefaultProgramInlineWait = 30 * time.Second

// ProgramInlineWait is the foreground budget in force: the default scaled by the
// verification runner's timeout allowance, so race instrumentation does not
// promote programs that would finish inline uninstrumented.
func ProgramInlineWait() time.Duration {
	return DefaultProgramInlineWait * time.Duration(runnerbudget.TimeoutScale())
}

// RunningProgramResult identifies the original program; no statements are replayed.
type RunningProgramResult struct {
	ProgramID string `json:"program_id"`
	State     string `json:"state"`
	Handle    string `json:"handle"`
}

func (r *ProgramRuntime) continueProgram(ctx context.Context, e *programEvaluation, ending *programEnding) (string, error) {
	run := e.run
	attached := make(chan error, 1)
	handle, err := r.Supervisor.Background.StartProgram(ctx, bgprocess.ProgramJobSpec{
		SessionID: run.tc.Identity.SessionID, RootSessionID: run.tc.ChatSessionID(),
		ProjectID: run.tc.Identity.ProjectID, Cwd: e.paths.Cwd().DisplayPath,
		CommandLine: run.source, OriginTool: run.tool, ToolCallID: run.tc.Identity.ToolCallID,
		Run: func(jobCtx context.Context, stdout, stderr io.Writer) (bgprocess.ProgramJobResult, error) {
			stop := context.AfterFunc(jobCtx, func() { r.Supervisor.Interrupt(run.root.Identity.ProgramID) })
			defer stop()
			attachErr := errors.Join(e.stdout.follow(stdout), e.stderr.follow(stderr))
			attached <- attachErr
			if attachErr != nil {
				r.Supervisor.Interrupt(run.root.Identity.ProgramID)
			}
			<-ending.done
			r.recordParkedRun(context.WithoutCancel(ctx), run)
			var outcome ProgramOutcome
			decodeErr := json.Unmarshal([]byte(ending.result), &outcome)
			status := outcome.Status
			if decodeErr != nil || (ending.err != nil && status == 0) {
				status = 1
			}
			if ending.result != "" {
				_, _ = fmt.Fprintln(stderr, ending.result)
			}
			return bgprocess.ProgramJobResult{ExitCode: status}, errors.Join(ending.err, decodeErr, attachErr)
		},
	})
	if err != nil {
		r.interruptProgram(ctx, run, ending)
		return "", err
	}
	if err := <-attached; err != nil {
		return "", err
	}
	if e.waits != nil {
		session := run.tc.Identity.SessionID
		e.waits.promoted(func() func() { return r.Supervisor.Background.BeginPersonWait(session, handle) })
	}
	e.call.detach()
	if out := e.callTC.Effects.Out; out != nil {
		out.MergeChild(run.tc.Effects.Out.TakeCommitted(), tools.MergeProgram)
		if projection, err := r.Recorder.ProjectProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, 1); err == nil {
			out.CommandProgram = &projection.Program
		}
	}
	result, err := surveyjson.Marshal(RunningProgramResult{ProgramID: run.root.Identity.ProgramID, State: "running", Handle: handle})
	return string(result), err
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
